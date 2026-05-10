package handlers

import (
	"encoding/json"
	"log"
	"net/http"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/the-financial-workspace/backend/internal/database"
	"github.com/the-financial-workspace/backend/internal/models"
	"github.com/the-financial-workspace/backend/internal/ws"
)

// ListCollaborators returns all collaborators for a budget (owner or
// collaborator). Each collaborator record is enriched with profile info.
//
// PERF: Runs in two queries total (down from three on the old
// verifyBudgetAccess + budget GET + collab GET path):
//  1. Combined UNION ALL of (budget owner if user has access) + (all
//     collaborator rows). One round-trip authorises the caller and lists
//     everyone.
//  2. Batch profile fetch by `id IN (...)` for the owner + collaborator
//     user_ids — one round-trip regardless of how many collaborators there
//     are (capped at 5 + 1 owner = 6).
//
// Not called on every request, so the profile fetch stays on the JSON-agg
// wrapper via database.DB.GetCtx.
func ListCollaborators(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	budgetID, ok := parseUUIDParam(c, "id")
	if !ok {
		return errBadRequest(c, "invalid budget ID")
	}

	reqCtx := c.Context()

	// Fetch budget owner + collaborators in a single round-trip. The old path
	// used verifyBudgetAccess + budget GET + collab GET = three DB hits.
	var ownerID uuid.UUID
	var collaborators []models.Collaborator

	rows, err := database.DB.Pool.Query(reqCtx, `
		SELECT 'owner' AS src, b.user_id AS user_id, NULL::uuid AS collab_id, NULL::text AS added_at, NULL::text AS role
		FROM budgets b
		WHERE b.id = $1
		  AND (b.user_id = $2 OR EXISTS (
		    SELECT 1 FROM budget_collaborators c WHERE c.budget_id = b.id AND c.user_id = $2
		  ))
		UNION ALL
		SELECT 'collab' AS src, c.user_id, c.id, c.added_at::text, c.role
		FROM budget_collaborators c
		WHERE c.budget_id = $1
		ORDER BY src DESC, added_at ASC NULLS FIRST
	`, budgetID, userID)
	if err != nil {
		return errInternal(c, "failed to fetch collaborators")
	}
	defer rows.Close()

	for rows.Next() {
		var src string
		var rowUserID uuid.UUID
		var collabID *uuid.UUID
		var addedAt *string
		var role *string
		if err := rows.Scan(&src, &rowUserID, &collabID, &addedAt, &role); err != nil {
			continue
		}
		if src == "owner" {
			ownerID = rowUserID
		} else {
			c := models.Collaborator{
				BudgetID: budgetID,
				UserID:   rowUserID,
				Role:     derefString(role),
				AddedAt:  derefString(addedAt),
			}
			if collabID != nil {
				c.ID = *collabID
			}
			collaborators = append(collaborators, c)
		}
	}
	if ownerID == uuid.Nil {
		return errNotFound(c, "budget not found")
	}

	// Prepend owner as synthetic entry.
	ownerEntry := models.Collaborator{
		ID:       ownerID,
		BudgetID: budgetID,
		UserID:   ownerID,
		Role:     "owner",
		AddedAt:  "",
	}
	result := make([]models.Collaborator, 0, 1+len(collaborators))
	result = append(result, ownerEntry)
	result = append(result, collaborators...)

	// Batch-fetch all profiles (owner + collaborators) in a single query.
	userIDs := make([]string, len(result))
	for i, collab := range result {
		userIDs[i] = collab.UserID.String()
	}

	profileQuery := database.NewFilter().
		Select("id,email,full_name,created_at,updated_at").
		In("id", userIDs).
		Build()

	profileBody, profileStatus, profileErr := database.DB.GetCtx(reqCtx, "profiles", profileQuery)
	if profileErr == nil && profileStatus == http.StatusOK {
		var profiles []models.Profile
		if err := json.Unmarshal(profileBody, &profiles); err == nil {
			profileMap := make(map[string]*models.Profile, len(profiles))
			for i := range profiles {
				profileMap[profiles[i].ID.String()] = &profiles[i]
			}
			for i := range result {
				if p, ok := profileMap[result[i].UserID.String()]; ok {
					result[i].Profile = p
				}
			}
		}
	}

	return c.JSON(result)
}

// RemoveCollaborator removes a collaborator from a budget. Only the budget
// owner can perform this action.
// On success it broadcasts a collaborator_removed event via WebSocket.
//
// PERF: Collapsed the previous verify-ownership + verify-collab-exists +
// delete-collab sequence (3 DB round-trips) into one atomic DELETE on the
// collaborators table that enforces ownership via an EXISTS subquery and
// returns the deleted row id. A separate, smaller EXISTS probe runs only
// if the DELETE affected zero rows, to disambiguate "not owner" from
// "not found" — preserving the pre-optimisation error surface.
func RemoveCollaborator(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	budgetID, ok := parseUUIDParam(c, "id")
	if !ok {
		return errBadRequest(c, "invalid budget ID")
	}

	targetUserID, ok := parseUUIDParam(c, "userId")
	if !ok {
		return errBadRequest(c, "invalid user ID")
	}

	// Prevent the owner from removing themselves as a collaborator (owner
	// is never a collaborator row anyway, but the explicit check preserves
	// the existing error surface for API consumers).
	if targetUserID == userID {
		return errBadRequest(c, "cannot remove yourself as the budget owner")
	}

	reqCtx := c.Context()

	// PERF: single atomic DELETE. The EXISTS subquery enforces owner-only
	// semantics and the primary WHERE enforces collab identity. RETURNING
	// id tells us whether a row was actually deleted.
	var deletedID uuid.UUID
	err := database.DB.Pool.QueryRow(reqCtx,
		`DELETE FROM budget_collaborators
		 WHERE budget_id = $1 AND user_id = $2
		   AND EXISTS(SELECT 1 FROM budgets WHERE id = $1 AND user_id = $3)
		 RETURNING id`,
		budgetID, targetUserID, userID,
	).Scan(&deletedID)
	if err != nil {
		// Zero-row case: disambiguate owner-mismatch vs collab-not-found so
		// the error surface matches the pre-optimisation behaviour.
		var isOwner bool
		_ = database.DB.Pool.QueryRow(reqCtx,
			`SELECT EXISTS(SELECT 1 FROM budgets WHERE id = $1 AND user_id = $2)`,
			budgetID, userID,
		).Scan(&isOwner)
		if !isOwner {
			return errForbidden(c, "only the budget owner can remove collaborators")
		}
		return errNotFound(c, "collaborator not found")
	}

	// Clean up links created by the removed collaborator for this budget.
	_, cleanupErr := database.DB.Pool.Exec(reqCtx,
		`DELETE FROM budget_links WHERE created_by = $1 AND (source_budget_id = $2 OR target_budget_id = $2)`,
		targetUserID, budgetID)
	if cleanupErr != nil {
		// Log but don't fail the request — collaborator removal succeeded.
		// Links will be orphaned but won't cause data integrity issues.
		log.Printf("[collaborators] link cleanup failed for budget %s user %s: %v",
			budgetID, targetUserID, cleanupErr)
	}
	// Invalidate every cached entry: we don't know which source budgets the
	// removed collaborator had links on without another query.
	invalidateLinkTargetsCacheAll()
	// The summary cache keys a spending_by_user row per collaborator, and the
	// removed user's expenses stay but attribution changes, so drop the
	// budget's cached summary/trends/resume too.
	invalidateBudget(budgetID)

	broadcast(budgetID.String(), ws.MessageTypeCollabRemoved, map[string]string{
		"user_id": targetUserID.String(),
	})

	return c.SendStatus(fiber.StatusNoContent)
}

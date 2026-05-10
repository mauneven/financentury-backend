package handlers

import (
	"encoding/json"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/the-financial-workspace/backend/internal/database"
	"github.com/the-financial-workspace/backend/internal/models"
)

// SaveDisplayOrder upserts a display order for the authenticated user.
// PUT /api/display-orders
//
// PERF: the whole call is already a single round-trip — INSERT ... ON
// CONFLICT DO UPDATE ... RETURNING *. The list of ordered IDs is stored as
// a JSONB column, so there's no per-item loop; the full array is shipped in
// one statement. No further DB-cost reduction is available here.
func SaveDisplayOrder(c *fiber.Ctx) error {
	userID, ok := requireUserID(c)
	if !ok {
		return errUnauthorized(c)
	}

	var req models.SaveDisplayOrderRequest
	if err := c.BodyParser(&req); err != nil {
		return errBadRequest(c, "invalid request body")
	}

	if req.ScopeKey == "" {
		return errBadRequest(c, "scope_key is required")
	}
	if len(req.ScopeKey) > 200 {
		return errBadRequest(c, "scope_key too long")
	}
	if len(req.OrderedIDs) > 500 {
		return errBadRequest(c, "too many ordered_ids")
	}

	// Validate each ID is a valid UUID, or — if it's a composite key like
	// "linked-..." — stays under the 200-char length ceiling.
	for _, id := range req.OrderedIDs {
		if _, err := uuid.Parse(id); err != nil {
			if len(id) > 200 {
				return errBadRequest(c, "ordered_id too long")
			}
		}
	}

	idsJSON, err := json.Marshal(req.OrderedIDs)
	if err != nil {
		return errInternal(c, "failed to marshal ordered_ids")
	}

	now := time.Now().UTC()
	var o models.DisplayOrder
	// PERF: use c.Context() so a cancelled HTTP request (e.g. client
	// disconnect during a drag-drop reorder) propagates to the DB instead
	// of holding a pool connection for a query whose result will be
	// discarded.
	err = database.DB.Pool.QueryRow(c.Context(),
		`INSERT INTO display_orders (id, user_id, scope_key, ordered_ids, updated_at)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (user_id, scope_key)
		 DO UPDATE SET ordered_ids = $4, updated_at = $5
		 RETURNING id, user_id, scope_key, ordered_ids, updated_at`,
		uuid.New(), userID, req.ScopeKey, idsJSON, now,
	).Scan(&o.ID, &o.UserID, &o.ScopeKey, &o.OrderedIDs, &o.UpdatedAt)
	if err != nil {
		return errInternal(c, "failed to save display order")
	}

	return c.JSON(o)
}

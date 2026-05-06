package webhook

import "github.com/gin-gonic/gin"

// RegisterRoutes mounts the webhook surface at the engine ROOT (not
// under /api/v1). Webhook URLs are caller-controlled by the provider's
// dashboard config — versioning is owned by the envelope's `type`
// discriminator, not the URL. Mirrors how Stripe / Shopify document
// their webhook endpoints (root path, no version prefix).
//
// Why root and not behind nginx's /api/ rate-limit zone: the provider's
// source IPs are not predictable (Stripe publishes a CIDR list that
// rotates) and the per-IP limits we apply to user traffic would
// throttle legitimate redelivery. Authentication is via signature
// verification, not network controls.
func RegisterRoutes(r *gin.Engine, h *Handler) {
	r.POST("/webhook/payment", h.HandlePayment)
}

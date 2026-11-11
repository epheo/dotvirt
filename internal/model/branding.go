package model

// Branding is a provider's dress for the console, read before sign-in: the
// login screen wears it too.
type Branding struct {
	// ProductName replaces dotvirt throughout the console; "" keeps it.
	ProductName string         `json:"productName,omitempty"`
	Links       []BrandingLink `json:"links,omitempty"`
	// Theme: /api/branding/theme.css answers a stylesheet loaded after the
	// console's own. Logo: /api/branding/logo answers an image.
	Theme bool `json:"theme,omitempty"`
	Logo  bool `json:"logo,omitempty"`
}

// BrandingLink is one masthead link back to the provider's own app.
type BrandingLink struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

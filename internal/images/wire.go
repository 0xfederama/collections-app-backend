package images

// The wire types below are declared outside handler.go, as in the other features.
// The responses reuse items.ItemJSON: an image operation answers with the item.

// SetItemImageURLRequest is the JSON body of setItemImage (published as "SetImageUrlRequest").
type SetItemImageURLRequest struct {
	URL string `json:"url" doc:"An absolute http or https URL of at most 2048 characters."`
}

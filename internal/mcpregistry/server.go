package mcpregistry

import "time"

type Server struct {
	Name        string    `json:"name"`
	Title       string    `json:"title,omitempty"`
	Description string    `json:"description"`
	Version     string    `json:"version"`
	WebsiteURL  string    `json:"website_url,omitempty"`
	Status      string    `json:"status"`
	UpdatedAt   time.Time `json:"updated_at"`
	Remotes     []Remote  `json:"remotes"`
	Icons       []Icon    `json:"icons"`
}

type Remote struct {
	Type    string   `json:"type"`
	URL     string   `json:"url"`
	Headers []Header `json:"headers,omitempty"`
}

type Icon struct {
	Src      string   `json:"src"`
	MimeType string   `json:"mime_type,omitempty"`
	Sizes    []string `json:"sizes,omitempty"`
	Theme    string   `json:"theme,omitempty"`
}

type Header struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	IsRequired  bool   `json:"is_required"`
	IsSecret    bool   `json:"is_secret"`
}

type SearchParams struct {
	Query     string
	RemoteURL string
	Limit     int
	Cursor    string
}

type SearchPage struct {
	Servers    []Server `json:"servers"`
	NextCursor *string  `json:"next_cursor"`
}

const (
	DefaultSearchLimit = 50
	MaxSearchLimit     = 100
)

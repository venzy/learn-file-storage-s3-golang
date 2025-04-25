package fileext

import "testing"

func TestFromContentType(t *testing.T) {
    tests := []struct {
        contentType string
        expected    string
    }{
        {"image/jpeg", ".jpg"},
        {"image/png", ".png"},
        {"image/gif", ".gif"},
        {"video/mp4", ".mp4"},
        {"image/webp", ""},
        {"application/json", ""},
        {"", ""},
    }

    for _, tt := range tests {
        result := FromMediaType(tt.contentType)
        if result != tt.expected {
            t.Errorf("FromContentType(%q) = %q; want %q", tt.contentType, result, tt.expected)
        }
    }
}
package files

import (
	"context"
	"fmt"
	"strings"
	"telesrv/internal/domain"
)

// CreateDocumentFromURLWithSpec persists the requested media attributes with the
// imported bytes, so reusing the returned file_id preserves its actual kind.
func (s *Service) CreateDocumentFromURLWithSpec(ctx context.Context, rawURL string, spec domain.DocumentSpec) (domain.Document, error) {
	if s == nil || s.externalMedia == nil {
		return domain.Document{}, ErrExternalMediaDisabled
	}
	data, mime, err := s.externalMedia.fetch(ctx, rawURL)
	if err != nil {
		return domain.Document{}, err
	}
	if strings.TrimSpace(spec.MimeType) == "" {
		spec.MimeType = mime
		if spec.MimeType == "" {
			spec.MimeType = "application/octet-stream"
		}
	}
	filename := false
	for _, attribute := range spec.Attributes {
		if attribute.Kind == domain.DocAttrFilename {
			filename = true
			break
		}
	}
	if !filename {
		spec.Attributes = append(spec.Attributes, domain.DocumentAttribute{Kind: domain.DocAttrFilename, FileName: externalMediaFilename(rawURL)})
	}
	doc, err := s.CreateDocumentFromBytes(ctx, data, spec)
	if err != nil {
		return domain.Document{}, fmt.Errorf("%w: %v", ErrExternalMediaInvalid, err)
	}
	return doc, nil
}

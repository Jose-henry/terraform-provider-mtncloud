package client

import (
	"context"
	"net/http"
)

// Certificate is an SSL certificate as GET /api/certificates/{id} returns it. The platform never
// returns the certificate or key text on a read.
type Certificate struct {
	ID          int64
	Name        string
	Description string
	DomainName  string
	CommonName  string
	TypeCode    string // "internal" for an uploaded certificate and key
	Wildcard    bool
	SelfSigned  bool
}

// CertificateInput is what an internal (uploaded) certificate is created or updated with. An empty
// Key or Chain isn't sent.
type CertificateInput struct {
	Name, Description, DomainName string
	Cert, Key, Chain              string
	Wildcard                      bool
}

type wireCertificate struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description"`
	DomainName  *string `json:"domainName"`
	CommonName  *string `json:"commonName"`
	Wildcard    bool    `json:"wildcard"`
	SelfSigned  bool    `json:"selfSigned"`
	Type        *struct {
		Code string `json:"code"`
	} `json:"type"`
}

func (w wireCertificate) cert() *Certificate {
	c := &Certificate{ID: w.ID, Name: w.Name, Description: deref(w.Description), DomainName: deref(w.DomainName),
		CommonName: deref(w.CommonName), Wildcard: w.Wildcard, SelfSigned: w.SelfSigned}
	if w.Type != nil {
		c.TypeCode = w.Type.Code
	}
	return c
}

// certBody is the body `certificates add -t internal -d` sends (2026-10-28).
func certBody(in CertificateInput, create bool) map[string]any {
	c := map[string]any{"name": in.Name, "description": in.Description, "domainName": in.DomainName,
		"certFile": in.Cert, "wildcard": in.Wildcard}
	if create {
		c["type"] = "internal"
	}
	if in.Key != "" {
		c["keyFile"] = in.Key
	}
	if in.Chain != "" {
		c["chainFile"] = in.Chain
	}
	return map[string]any{"certificate": c}
}

// The create and update replies echo the key file: only the ID is decoded from them.
type certReply struct {
	Certificate struct {
		ID int64 `json:"id"`
	} `json:"certificate"`
}

// CreateCertificate uploads an internal certificate and returns its ID.
func (c *Client) CreateCertificate(ctx context.Context, in CertificateInput) (int64, error) {
	var resp certReply
	if err := c.do(ctx, http.MethodPost, "/api/certificates", nil, certBody(in, true), &resp); err != nil {
		return 0, err
	}
	return resp.Certificate.ID, nil
}

// GetCertificate reads one certificate.
func (c *Client) GetCertificate(ctx context.Context, id int64) (*Certificate, error) {
	var resp struct {
		Certificate wireCertificate `json:"certificate"`
	}
	if err := c.get(ctx, "/api/certificates/"+itoa(id), nil, &resp); err != nil {
		return nil, err
	}
	return resp.Certificate.cert(), nil
}

// FindCertificatesNamed lists the certificates with this exact name.
func (c *Client) FindCertificatesNamed(ctx context.Context, name string) ([]*Certificate, error) {
	items, err := listAll[wireCertificate](ctx, c, "/api/certificates", "certificates", nil)
	if err != nil {
		return nil, err
	}
	var out []*Certificate
	for _, w := range items {
		if w.Name == name {
			out = append(out, w.cert())
		}
	}
	return out, nil
}

// UpdateCertificate changes a certificate.
func (c *Client) UpdateCertificate(ctx context.Context, id int64, in CertificateInput) error {
	var resp certReply
	return c.do(ctx, http.MethodPut, "/api/certificates/"+itoa(id), nil, certBody(in, false), &resp)
}

// DeleteCertificate deletes a certificate; one already gone is not an error.
func (c *Client) DeleteCertificate(ctx context.Context, id int64) error {
	return ignoreNotFound(c.do(ctx, http.MethodDelete, "/api/certificates/"+itoa(id), nil, nil, nil))
}

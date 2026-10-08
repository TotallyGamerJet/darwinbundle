package macbundle_test

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"software.sslmate.com/src/go-pkcs12"

	"github.com/TotallyGamerJet/macbundle"
)

// testTeam is the Team ID the test identities carry in their certificates'
// organizational unit, which is where a Team ID lives.
const testTeam = "TEAM123456"

// identity is a self-signed code-signing certificate and its key: enough to
// produce a signature that carries a team, which is what separates it from an
// ad hoc one. Nothing trusts it, and nothing here needs anything to.
type identity struct {
	key  *ecdsa.PrivateKey
	cert *x509.Certificate
}

func newIdentity(t testing.TB) *identity {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName:         "macbundle test identity (" + testTeam + ")",
			OrganizationalUnit: []string{testTeam},
			Organization:       []string{"macbundle tests"},
		},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &identity{key: key, cert: cert}
}

// signer adapts the identity to macbundle.Signer, with the chain in the order
// the interface promises: the leaf first.
func (i *identity) signer() *softSigner { return &softSigner{id: i} }

type softSigner struct{ id *identity }

var _ macbundle.Signer = (*softSigner)(nil)

func (s *softSigner) Public() crypto.PublicKey { return s.id.key.Public() }

func (s *softSigner) Sign(_ io.Reader, digest []byte, opts crypto.SignerOpts) ([]byte, error) {
	return s.id.key.Sign(rand.Reader, digest, opts)
}

func (s *softSigner) Certificates() []*x509.Certificate { return []*x509.Certificate{s.id.cert} }

// p12 writes the identity to a PKCS#12 file and returns its path.
func (i *identity) p12(t testing.TB, password string) string {
	t.Helper()
	data, err := pkcs12.Modern.Encode(i.key, i.cert, nil, password)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "identity.p12")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

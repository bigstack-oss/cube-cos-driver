package enterprise

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha512"
	"crypto/x509"
	"encoding/asn1"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/cloudflare/circl/sign/mldsa/mldsa87"
)

type testKeys struct {
	ec  *ecdsa.PrivateKey
	ml  *mldsa87.PrivateKey
	pub *TrustKey
}

func newTestKeys(t *testing.T) testKeys {
	ec, _ := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&ec.PublicKey)
	mlPub, mlPriv, _ := mldsa87.GenerateKey(rand.Reader)
	raw, _ := mlPub.MarshalBinary()
	spki, _ := asn1.Marshal(struct {
		Alg struct{ OID asn1.ObjectIdentifier }
		Key asn1.BitString
	}{struct{ OID asn1.ObjectIdentifier }{asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 3, 19}}, asn1.BitString{Bytes: raw, BitLength: len(raw) * 8}})
	return testKeys{ec, mlPriv, &TrustKey{
		P384:    string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})),
		MLDSA87: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: spki})),
	}}
}

func tarOf(t *testing.T, files map[string][]byte) []byte {
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	for n, d := range files {
		tw.WriteHeader(&tar.Header{Name: n, Mode: 0o644, Size: int64(len(d))})
		tw.Write(d)
	}
	tw.Close()
	return b.Bytes()
}

// signedRelease returns release-dir files for ver, ECDSA-signed by ec and ML-DSA-signed by ml.
func signedRelease(t *testing.T, ver string, ec, ml testKeys, drop ...string) map[string][]byte {
	msg := []byte("cube-advisor-agent_linux_amd64 sha256:00 " + ver + "\n")
	h := sha512.Sum384(msg)
	ecSig, _ := ecdsa.SignASN1(rand.Reader, ec.ec, h[:])
	mlSig := make([]byte, mldsa87.SignatureSize)
	if err := mldsa87.SignTo(ml.ml, msg, nil, false, mlSig); err != nil {
		t.Fatal(err)
	}
	pre := "srv/advisor/releases/" + ver + "/"
	rel := map[string][]byte{pre + "manifest.txt": msg, pre + "manifest.txt.sig": ecSig, pre + "manifest.txt.mldsa87.sig": mlSig}
	for _, d := range drop {
		delete(rel, pre+d)
	}
	return rel
}

// writePigz wraps release files into a minimal advisor .pigz.
func writePigz(t *testing.T, rel map[string][]byte) string {
	image := tarOf(t, map[string][]byte{"blobs/sha256/aa": tarOf(t, rel), "index.json": []byte("{}")})
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write(tarOf(t, map[string][]byte{"cube-advisor-api.tar": image, "import.sh": []byte("#!/bin/sh\n")}))
	zw.Close()
	p := filepath.Join(t.TempDir(), "cube-advisor-0.4.25.pigz")
	os.WriteFile(p, gz.Bytes(), 0o644)
	return p
}

// writeBundle builds a one-release bundle signed by signer; drop names files to leave out.
func writeBundle(t *testing.T, signer testKeys, drop ...string) string {
	return writePigz(t, signedRelease(t, "0.4.25", signer, signer, drop...))
}

func TestVerifyAdvisorBundle_OK(t *testing.T) {
	k := newTestKeys(t)
	vs, err := VerifyAdvisorBundle(writeBundle(t, k), k.pub)
	if err != nil || len(vs) != 1 || vs[0] != "0.4.25" {
		t.Fatalf("got %v %v", vs, err)
	}
}

func TestVerifyAdvisorBundle_WrongKey(t *testing.T) {
	k, other := newTestKeys(t), newTestKeys(t)
	_, err := VerifyAdvisorBundle(writeBundle(t, other), k.pub)
	if err == nil || !strings.Contains(err.Error(), "0.4.25") {
		t.Fatalf("dev-signed bundle must fail naming the release, got %v", err)
	}
}

func TestVerifyAdvisorBundle_MissingMLDSA(t *testing.T) {
	k := newTestKeys(t)
	_, err := VerifyAdvisorBundle(writeBundle(t, k, "manifest.txt.mldsa87.sig"), k.pub)
	if err == nil || !strings.Contains(err.Error(), "mldsa87") {
		t.Fatalf("got %v", err)
	}
}

func TestVerifyAdvisorBundle_NoRelease(t *testing.T) {
	k := newTestKeys(t)
	_, err := VerifyAdvisorBundle(writeBundle(t, k, "manifest.txt", "manifest.txt.sig", "manifest.txt.mldsa87.sig"), k.pub)
	if err == nil || !strings.Contains(err.Error(), "no signed agent release") {
		t.Fatalf("got %v", err)
	}
}

func TestVerifyAdvisorBundle_MissingECDSASig(t *testing.T) {
	k := newTestKeys(t)
	_, err := VerifyAdvisorBundle(writeBundle(t, k, "manifest.txt.sig"), k.pub)
	if err == nil || !strings.Contains(err.Error(), "manifest.txt.sig") {
		t.Fatalf("got %v", err)
	}
}

func TestVerifyAdvisorBundle_MLDSAFromOtherKey(t *testing.T) {
	k, other := newTestKeys(t), newTestKeys(t)
	p := writePigz(t, signedRelease(t, "0.4.25", k, other))
	_, err := VerifyAdvisorBundle(p, k.pub)
	if err == nil || !strings.Contains(err.Error(), "ML-DSA-87") {
		t.Fatalf("got %v", err)
	}
}

func TestVerifyAdvisorBundle_UnsignedSecondRelease(t *testing.T) {
	k := newTestKeys(t)
	rel := signedRelease(t, "0.4.25", k, k)
	rel["srv/advisor/releases/0.4.26/cube-advisor-agent_linux_amd64"] = []byte("bin")
	_, err := VerifyAdvisorBundle(writePigz(t, rel), k.pub)
	if err == nil || !strings.Contains(err.Error(), "0.4.26") {
		t.Fatalf("got %v", err)
	}
}

func TestVerifyAdvisorBundle_RejectsWrongCurve(t *testing.T) {
	k := newTestKeys(t)
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&ec.PublicKey)
	bad := &TrustKey{P384: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})), MLDSA87: k.pub.MLDSA87}
	if _, err := VerifyAdvisorBundle(writeBundle(t, k), bad); err == nil || !strings.Contains(err.Error(), "P-384") {
		t.Fatalf("got %v", err)
	}
}

func TestVerifyAdvisorBundle_RejectsWrongMLDSAOID(t *testing.T) {
	k := newTestKeys(t)
	blk, _ := pem.Decode([]byte(k.pub.MLDSA87))
	var spki struct {
		Alg struct{ OID asn1.ObjectIdentifier }
		Key asn1.BitString
	}
	asn1.Unmarshal(blk.Bytes, &spki)
	spki.Alg.OID = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 3, 18}
	der, _ := asn1.Marshal(spki)
	bad := &TrustKey{P384: k.pub.P384, MLDSA87: string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))}
	if _, err := VerifyAdvisorBundle(writeBundle(t, k), bad); err == nil || !strings.Contains(err.Error(), "ML-DSA-87") {
		t.Fatalf("got %v", err)
	}
}

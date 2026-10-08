package enterprise

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha512"
	"crypto/x509"
	"encoding/asn1"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/cloudflare/circl/sign/mldsa/mldsa87"
)

const releasesPrefix = "srv/advisor/releases/"

// VerifyAdvisorBundle checks every agent release baked into an Advisor .pigz against key.
func VerifyAdvisorBundle(pigzPath string, key *TrustKey) ([]string, error) {
	ecPub, mlPub, err := parseTrustKey(key)
	if err != nil {
		return nil, err
	}
	rels, err := readBundleReleases(pigzPath)
	if err != nil {
		return nil, err
	}
	if len(rels) == 0 {
		return nil, errors.New("no signed agent release found in the advisor bundle")
	}
	vs := make([]string, 0, len(rels))
	for v := range rels {
		vs = append(vs, v)
	}
	sort.Strings(vs)
	for _, v := range vs {
		f := rels[v]
		msg, ecSig, mlSig := f["manifest.txt"], f["manifest.txt.sig"], f["manifest.txt.mldsa87.sig"]
		switch {
		case msg == nil:
			return nil, fmt.Errorf("release %s: no manifest.txt", v)
		case ecSig == nil:
			return nil, fmt.Errorf("release %s: no manifest.txt.sig", v)
		case mlSig == nil:
			return nil, fmt.Errorf("release %s: no manifest.txt.mldsa87.sig", v)
		}
		h := sha512.Sum384(msg)
		if !ecdsa.VerifyASN1(ecPub, h[:], ecSig) {
			return nil, fmt.Errorf("release %s: ECDSA signature does not verify against this CubeCOS release's advisor key", v)
		}
		if !mldsa87.Verify(mlPub, msg, nil, mlSig) {
			return nil, fmt.Errorf("release %s: ML-DSA-87 signature does not verify against this CubeCOS release's advisor key", v)
		}
	}
	return vs, nil
}

func parseTrustKey(k *TrustKey) (*ecdsa.PublicKey, *mldsa87.PublicKey, error) {
	b, _ := pem.Decode([]byte(k.P384))
	if b == nil {
		return nil, nil, errors.New("advisor trust key: bad P-384 PEM")
	}
	pub, err := x509.ParsePKIXPublicKey(b.Bytes)
	if err != nil {
		return nil, nil, err
	}
	ec, ok := pub.(*ecdsa.PublicKey)
	if !ok || ec.Curve != elliptic.P384() {
		return nil, nil, errors.New("advisor trust key: not an ECDSA P-384 key")
	}
	mb, _ := pem.Decode([]byte(k.MLDSA87))
	if mb == nil {
		return nil, nil, errors.New("advisor trust key: bad ML-DSA-87 PEM")
	}
	var spki struct {
		Alg struct{ OID asn1.ObjectIdentifier }
		Key asn1.BitString
	}
	if rest, err := asn1.Unmarshal(mb.Bytes, &spki); err != nil {
		return nil, nil, err
	} else if len(rest) != 0 {
		return nil, nil, errors.New("advisor trust key: trailing data after ML-DSA-87 key")
	}
	if !spki.Alg.OID.Equal(asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 3, 19}) {
		return nil, nil, errors.New("advisor trust key: not an ML-DSA-87 key")
	}
	var ml mldsa87.PublicKey
	if err := ml.UnmarshalBinary(spki.Key.Bytes); err != nil {
		return nil, nil, err
	}
	return ec, &ml, nil
}

// readBundleReleases returns version -> file name -> content for every release dir in the bundle.
func readBundleReleases(pigzPath string) (map[string]map[string][]byte, error) {
	f, err := os.Open(pigzPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	outer := tar.NewReader(zr)
	for {
		h, err := outer.Next()
		if err == io.EOF {
			return nil, errors.New("advisor bundle has no cube-advisor-api.tar")
		}
		if err != nil {
			return nil, err
		}
		if h.Name != "cube-advisor-api.tar" {
			continue
		}
		return scanImage(tar.NewReader(outer))
	}
}

func scanImage(img *tar.Reader) (map[string]map[string][]byte, error) {
	out := map[string]map[string][]byte{}
	for {
		h, err := img.Next()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if !strings.HasPrefix(h.Name, "blobs/") || h.Typeflag != tar.TypeReg {
			continue
		}
		layer := tar.NewReader(img)
		for {
			lh, err := layer.Next()
			if err != nil {
				break // not a tar (config/manifest json) or end of layer
			}
			name := strings.TrimPrefix(lh.Name, "./")
			if !strings.HasPrefix(name, releasesPrefix) {
				continue
			}
			dir, file := path.Split(strings.TrimPrefix(name, releasesPrefix))
			v := strings.TrimSuffix(dir, "/")
			if v == "" || strings.Contains(v, "/") || file == "" || lh.Typeflag != tar.TypeReg {
				continue
			}
			if out[v] == nil {
				out[v] = map[string][]byte{}
			}
			if !strings.HasPrefix(file, "manifest.txt") {
				continue
			}
			var b bytes.Buffer
			if _, err := io.Copy(&b, layer); err != nil {
				return nil, err
			}
			out[v][file] = b.Bytes()
		}
	}
}

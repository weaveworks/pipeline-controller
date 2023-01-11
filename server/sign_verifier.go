package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/sha512"
	"errors"
	"fmt"
	"hash"
	"net/http"
	"strings"

	pipelinev1alpha1 "github.com/weaveworks/pipeline-controller/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func verifyXSignature(ctx context.Context, k8sClient client.Client, p pipelinev1alpha1.Pipeline, header http.Header, body []byte) error {
	// If not secret defined just ignore the X-Signature checking
	if p.Spec.Promotion == nil || p.Spec.Promotion.Strategy.SecretRef == nil {
		return nil
	}

	if len(header[SignatureHeader]) == 0 {
		return errors.New("no X-Signature header provided")
	}

	s := &corev1.Secret{
		ObjectMeta: v1.ObjectMeta{
			Name:      p.Spec.Promotion.Strategy.SecretRef.Name,
			Namespace: p.Namespace,
		},
	}

	if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(s), s); err != nil {
		return fmt.Errorf("failed fetching Secret %s/%s: %w", s.Namespace, s.Name, err)
	}

	key := s.Data["hmac-key"]
	if len(key) == 0 {
		return fmt.Errorf("no 'hmac-key' field present in %s/%s Spec.AppRef.SecretRef", p.Namespace, s.Name)
	}

	if err := validateSignature(header[SignatureHeader][0], body, key); err != nil {
		return fmt.Errorf("failed verifying X-Signature header: %s", err)
	}

	return nil
}

func validateSignature(sig string, payload, key []byte) error {
	sigHdr := strings.Split(sig, "=")
	if len(sigHdr) != 2 {
		return fmt.Errorf("invalid signature value")
	}

	var newF func() hash.Hash

	switch sigHdr[0] {
	case "sha224":
		newF = sha256.New224
	case "sha256":
		newF = sha256.New
	case "sha384":
		newF = sha512.New384
	case "sha512":
		newF = sha512.New
	default:
		return fmt.Errorf("unsupported signature algorithm %q", sigHdr[0])
	}

	mac := hmac.New(newF, key)
	if _, err := mac.Write(payload); err != nil {
		return fmt.Errorf("error MAC'ing payload: %w", err)
	}

	sum := fmt.Sprintf("%x", mac.Sum(nil))
	if sum != sigHdr[1] {
		return fmt.Errorf("HMACs don't match: %#v != %#v", sum, sigHdr[1])
	}

	return nil
}

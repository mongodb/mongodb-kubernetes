package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	omv1 "github.com/mongodb/mongodb-kubernetes/api/mongodb/v1/om"
)

const (
	licenseAdminPath          = "admin/licenses"
	licenseRecomputePath      = "admin/licenses/recompute"
	licenseReleasePath        = "admin/licenses/%s"
	licenseReceiptPath        = "admin/artifacts/%s/receipt"
	licenseIdentityPath       = "admin/artifacts/identity"
	licenseIdentityRotatePath = "admin/artifacts/identity/rotate"
)

// LicenseAdmin is the Ops Manager Admin EA licence API.
type LicenseAdmin interface {
	ReadLicenseState(ctx context.Context) (omv1.OpsManagerLicenseStatus, error)
	InstallLicense(ctx context.Context, rawArtifact json.RawMessage) (omv1.OpsManagerLicenseStatus, error)
	ReleaseLicense(ctx context.Context, artifactID string) (omv1.OpsManagerLicenseStatus, error)
	RecomputeLicense(ctx context.Context) error
	ReadLicenseReceipt(ctx context.Context, artifactID string) (string, error)
	ReadLicenseIdentity(ctx context.Context) (IdentityView, error)
	RotateLicenseIdentity(ctx context.Context) error
}

type licenseReceiptView struct {
	Receipt json.RawMessage `json:"receipt"`
}

type licenseRotationView struct {
	Outcome string `json:"outcome"`
}

type IdentityView struct {
	HolderID string `json:"holderId"`
}

// ArtifactIDFromRaw reads the artifact_id claim out of a raw signed artifact: the envelope's
// base64 payload decodes to the claims JSON the signer minted. Reading it from the bytes the
// customer installs lets the operator identify its artifact without depending on how many
// licenses OM's state happens to list.
func ArtifactIDFromRaw(rawArtifact string) (string, error) {
	var envelope struct {
		Payload string `json:"payload"`
	}
	if err := json.Unmarshal([]byte(rawArtifact), &envelope); err != nil {
		return "", errors.New("license artifact is not a valid JSON envelope")
	}
	if envelope.Payload == "" {
		return "", errors.New("license artifact has no payload")
	}
	payload, err := base64.StdEncoding.DecodeString(envelope.Payload)
	if err != nil {
		if payload, err = base64.RawStdEncoding.DecodeString(envelope.Payload); err != nil {
			return "", errors.New("license artifact payload is not base64")
		}
	}
	var claims struct {
		ArtifactID string `json:"artifact_id"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", errors.New("license artifact payload is not valid JSON")
	}
	if claims.ArtifactID == "" {
		return "", errors.New("license artifact claims carry no artifact_id")
	}
	return claims.ArtifactID, nil
}

func (a *DefaultOmAdmin) ReadLicenseState(ctx context.Context) (omv1.OpsManagerLicenseStatus, error) {
	body, _, err := a.get(ctx, licenseAdminPath)
	if err != nil {
		return omv1.OpsManagerLicenseStatus{}, err
	}
	return decodeLicenseState(body)
}

func (a *DefaultOmAdmin) InstallLicense(ctx context.Context, rawArtifact json.RawMessage) (omv1.OpsManagerLicenseStatus, error) {
	body, _, err := a.put(ctx, licenseAdminPath, rawArtifact)
	if err != nil {
		return omv1.OpsManagerLicenseStatus{}, err
	}
	return decodeLicenseState(body)
}

func (a *DefaultOmAdmin) ReleaseLicense(ctx context.Context, artifactID string) (omv1.OpsManagerLicenseStatus, error) {
	body, _, err := a.httpVerb(ctx, "DELETE", licenseReleasePath, nil, artifactID)
	if err != nil {
		return omv1.OpsManagerLicenseStatus{}, err
	}
	return decodeLicenseState(body)
}

func (a *DefaultOmAdmin) RecomputeLicense(ctx context.Context) error {
	_, _, err := a.post(ctx, licenseRecomputePath, nil)
	return err
}

func (a *DefaultOmAdmin) ReadLicenseReceipt(ctx context.Context, artifactID string) (string, error) {
	body, _, err := a.get(ctx, licenseReceiptPath, artifactID)
	if err != nil {
		return "", err
	}
	var view licenseReceiptView
	if json.Unmarshal(body, &view) != nil || len(view.Receipt) == 0 {
		return string(body), nil
	}
	return string(view.Receipt), nil
}

func (a *DefaultOmAdmin) ReadLicenseIdentity(ctx context.Context) (IdentityView, error) {
	body, _, err := a.get(ctx, licenseIdentityPath)
	if err != nil {
		return IdentityView{}, err
	}
	var view IdentityView
	if err := json.Unmarshal(body, &view); err != nil {
		return IdentityView{}, err
	}
	return view, nil
}

func (a *DefaultOmAdmin) RotateLicenseIdentity(ctx context.Context) error {
	body, _, err := a.post(ctx, licenseIdentityRotatePath, nil)
	if err != nil {
		return err
	}
	var view licenseRotationView
	if err := json.Unmarshal(body, &view); err != nil {
		return err
	}
	if view.Outcome != "ROTATED" {
		return fmt.Errorf("license identity rotation returned %s", view.Outcome)
	}
	return nil
}

func decodeLicenseState(body []byte) (omv1.OpsManagerLicenseStatus, error) {
	if len(body) == 0 {
		return omv1.OpsManagerLicenseStatus{}, errors.New("empty license state body")
	}
	var state omv1.OpsManagerLicenseStatus
	if err := json.Unmarshal(body, &state); err != nil {
		return omv1.OpsManagerLicenseStatus{}, fmt.Errorf("invalid license state body: %w", err)
	}
	return state, nil
}

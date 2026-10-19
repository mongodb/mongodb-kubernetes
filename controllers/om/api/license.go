package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
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
	ReadLicenseState(ctx context.Context) (LicenseView, error)
	InstallLicense(ctx context.Context, rawArtifact json.RawMessage) (LicenseView, error)
	ReleaseLicense(ctx context.Context, artifactID string) (LicenseView, error)
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

// LicenseView is the license state Ops Manager returns.
type LicenseView struct {
	Enforcement        *LicenseEnforcementView             `json:"enforcement,omitempty"`
	Licenses           []LicenseArtifactView               `json:"licenses,omitempty"`
	ActiveEntitlements map[string][]LicenseEntitlementView `json:"activeEntitlements,omitempty"`
}

// ArtifactIDs returns every artifact id the view lists, released rows included. It is for logging.
func (s LicenseView) ArtifactIDs() []string {
	artifactIDs := make([]string, 0, len(s.Licenses))
	for _, license := range s.Licenses {
		if license.ArtifactID != "" {
			artifactIDs = append(artifactIDs, license.ArtifactID)
		}
	}
	return artifactIDs
}

// LicenseArtifactReleased marks an artifact Ops Manager no longer holds.
const LicenseArtifactReleased = "RELEASED"

// ArtifactStatus returns the status Ops Manager reports for the artifact, and whether the view lists it at all.
func (s LicenseView) ArtifactStatus(artifactID string) (string, bool) {
	for _, license := range s.Licenses {
		if license.ArtifactID == artifactID {
			return license.Status, true
		}
	}
	return "", false
}

// ListsArtifact reports whether the view carries a row for the artifact, whatever its status.
func (s LicenseView) ListsArtifact(artifactID string) bool {
	_, listed := s.ArtifactStatus(artifactID)
	return listed
}

// HoldsArtifact reports whether Ops Manager holds the artifact. A released artifact keeps its row
// and does not count as held, while a denied one is installed and does.
func (s LicenseView) HoldsArtifact(artifactID string) bool {
	status, listed := s.ArtifactStatus(artifactID)
	return listed && status != LicenseArtifactReleased
}

// HeldArtifactIDs returns the artifacts Ops Manager holds.
func (s LicenseView) HeldArtifactIDs() []string {
	ids := make([]string, 0, len(s.Licenses))
	for _, license := range s.Licenses {
		if license.ArtifactID != "" && license.Status != LicenseArtifactReleased {
			ids = append(ids, license.ArtifactID)
		}
	}
	return ids
}

type LicenseEntitlementView struct {
	Type        string                        `json:"type"`
	ProductName string                        `json:"productName,omitempty"`
	Active      bool                          `json:"active"`
	Window      *LicenseEntitlementWindowView `json:"window,omitempty"`
}

type LicenseEntitlementWindowView struct {
	Start string `json:"start,omitempty"`
	End   string `json:"end,omitempty"`
}

type LicenseEnforcementView struct {
	EffectiveState string                            `json:"effectiveState"`
	Cause          string                            `json:"cause"`
	Enforced       bool                              `json:"enforced"`
	Transition     *LicenseEnforcementTransitionView `json:"transition,omitempty"`
	Warnings       []LicenseWarningView              `json:"warnings,omitempty"`
	Verdicts       []LicenseVerdictView              `json:"verdicts,omitempty"`
}

type LicenseEnforcementTransitionView struct {
	EvaluatorID  string `json:"evaluatorId,omitempty"`
	InStateUntil string `json:"inStateUntil,omitempty"`
	NextState    string `json:"nextState,omitempty"`
}

type LicenseWarningView struct {
	EvaluatorID string                      `json:"evaluatorId,omitempty"`
	Since       string                      `json:"since,omitempty"`
	Subjects    []LicenseWarningSubjectView `json:"subjects,omitempty"`
}

type LicenseWarningSubjectView struct {
	ID    string `json:"id,omitempty"`
	Since string `json:"since,omitempty"`
}

type LicenseVerdictView struct {
	EvaluatorID  string                        `json:"evaluatorId,omitempty"`
	State        string                        `json:"state,omitempty"`
	BreachAt     string                        `json:"breachAt,omitempty"`
	WarningStart string                        `json:"warningStart,omitempty"`
	Transition   *LicenseVerdictTransitionView `json:"transition,omitempty"`
	Metadata     map[string]any                `json:"metadata,omitempty"`
}

type LicenseVerdictTransitionView struct {
	InStateUntil string `json:"inStateUntil,omitempty"`
	NextState    string `json:"nextState,omitempty"`
}

type LicenseArtifactView struct {
	ArtifactID   string             `json:"artifactId"`
	Usable       bool               `json:"usable"`
	GeneratedAt  string             `json:"generatedAt,omitempty"`
	InstalledAt  string             `json:"installedAt,omitempty"`
	Status       string             `json:"status,omitempty"`
	StatusCause  string             `json:"statusCause,omitempty"`
	StatusReason string             `json:"statusReason,omitempty"`
	TermEnd      string             `json:"termEnd,omitempty"`
	Grants       []LicenseGrantView `json:"grants,omitempty"`
}

type LicenseGrantView struct {
	GrantType    string                   `json:"grantType,omitempty"`
	GrantID      string                   `json:"grantId,omitempty"`
	StartDate    string                   `json:"startDate,omitempty"`
	EndDate      string                   `json:"endDate,omitempty"`
	Entitlements []LicenseEntitlementView `json:"entitlements,omitempty"`
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

func (a *DefaultOmAdmin) ReadLicenseState(ctx context.Context) (LicenseView, error) {
	body, _, err := a.get(ctx, licenseAdminPath)
	if err != nil {
		return LicenseView{}, err
	}
	return decodeLicenseState(body)
}

func (a *DefaultOmAdmin) InstallLicense(ctx context.Context, rawArtifact json.RawMessage) (LicenseView, error) {
	body, _, err := a.put(ctx, licenseAdminPath, rawArtifact)
	if err != nil {
		return LicenseView{}, err
	}
	return decodeLicenseState(body)
}

func (a *DefaultOmAdmin) ReleaseLicense(ctx context.Context, artifactID string) (LicenseView, error) {
	body, _, err := a.httpVerb(ctx, "DELETE", licenseReleasePath, nil, artifactID)
	if err != nil {
		return LicenseView{}, err
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

func decodeLicenseState(body []byte) (LicenseView, error) {
	if len(body) == 0 {
		return LicenseView{}, errors.New("empty license state body")
	}
	var state LicenseView
	if err := json.Unmarshal(body, &state); err != nil {
		return LicenseView{}, fmt.Errorf("invalid license state body: %w", err)
	}
	return state, nil
}

package workload

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/blisspixel/fitr/internal/record"
)

const digestPrefix = "sha256:"

const maximumRequestedContext = 16 * 1024 * 1024

type SealedPlan struct {
	Plan       Plan
	privateKey ed25519.PrivateKey
}

func NewPlan(model record.ModelIdentity, deviceKey string, trials, maxTurns,
	timeoutSeconds, requestedContext int) (*SealedPlan, error) {
	if model.RuntimeBoundDigest() == "" {
		return nil, errors.New("workload plan requires a runtime-bound model artifact")
	}
	if strings.TrimSpace(deviceKey) == "" {
		return nil, errors.New("workload plan requires a device key")
	}
	if err := ValidatePlanBounds(trials, maxTurns, timeoutSeconds, requestedContext); err != nil {
		return nil, err
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("workload plan signing key: %w", err)
	}
	plan := Plan{
		Schema: PlanSchema, CompletionKey: base64.RawStdEncoding.EncodeToString(publicKey),
		Workflow: WorkflowID, WorkflowVersion: WorkflowVersion, Model: model, DeviceKey: deviceKey,
		Trials: trials, MaxTurns: maxTurns, TimeoutSeconds: timeoutSeconds,
		RequestedContext: requestedContext, Retention: RetainHashesAndVerifier,
	}
	contract := policyRepairContract()
	plan.Contract = &contract
	digest, err := planDigest(plan)
	if err != nil {
		return nil, err
	}
	plan.PlanSHA256 = digest
	return &SealedPlan{Plan: plan, privateKey: privateKey}, nil
}

func (plan Plan) Validate() error {
	if (plan.Schema != PlanSchema && plan.Schema != LegacyPlanSchema) ||
		plan.Workflow != WorkflowID || plan.WorkflowVersion != WorkflowVersion {
		return errors.New("unsupported workload plan schema or workflow")
	}
	if plan.Schema == PlanSchema {
		if plan.Contract == nil || *plan.Contract != policyRepairContract() {
			return errors.New("workload plan contract does not match the supported workflow")
		}
	} else if plan.Contract != nil {
		return errors.New("legacy workload plan cannot carry a workflow contract")
	}
	if plan.Retention != RetainHashesAndVerifier {
		return fmt.Errorf("unsupported workload retention policy %q", plan.Retention)
	}
	if plan.Model.RuntimeBoundDigest() == "" || strings.TrimSpace(plan.DeviceKey) == "" {
		return errors.New("workload plan identity is incomplete")
	}
	if err := ValidatePlanBounds(plan.Trials, plan.MaxTurns, plan.TimeoutSeconds, plan.RequestedContext); err != nil {
		return fmt.Errorf("workload plan bounds: %w", err)
	}
	publicKey, err := base64.RawStdEncoding.DecodeString(plan.CompletionKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return errors.New("workload plan completion key is invalid")
	}
	digest, err := planDigest(plan)
	if err != nil {
		return err
	}
	if digest != plan.PlanSHA256 {
		return errors.New("workload plan digest does not match")
	}
	return nil
}

func ValidatePlanBounds(trials, maxTurns, timeoutSeconds, requestedContext int) error {
	if trials < 1 || trials > 20 {
		return errors.New("trials must be between 1 and 20")
	}
	if maxTurns < 1 || maxTurns > 40 {
		return errors.New("max turns must be between 1 and 40")
	}
	if timeoutSeconds < 1 || timeoutSeconds > 3600 {
		return errors.New("timeout must be between 1 and 3600 seconds")
	}
	if requestedContext < 1 || requestedContext > maximumRequestedContext {
		return fmt.Errorf("requested context must be between 1 and %d tokens", maximumRequestedContext)
	}
	return nil
}

func planDigest(plan Plan) (string, error) {
	plan.PlanSHA256 = ""
	return hashValue(plan.Schema, plan)
}

func hashValue(domain string, value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	hash := sha256.New()
	_, _ = hash.Write([]byte(domain + "\x00"))
	_, _ = hash.Write(data)
	return digestPrefix + hex.EncodeToString(hash.Sum(nil)), nil
}

func validSHA256(value string) bool {
	encoded, ok := strings.CutPrefix(value, digestPrefix)
	if !ok || len(encoded) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(encoded)
	return err == nil
}

package configuration

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"surplustoken-connect/internal/configedit"
)

const providerScope = "providers.surplustoken"

func managedFixture(t *testing.T) (*Engine, string) {
	t.Helper()
	e, path := fixture(t)
	e.Paths = map[string]string{"opencode": path}
	return e, path
}

func providerPatches(model, key, protocol string) []configedit.KV {
	field := providerScope + ".models." + strings.ReplaceAll(model, ".", `\.`)
	patches := []configedit.KV{
		{Path: providerScope + ".package", Value: protocol},
		{Path: providerScope + ".settings.apiKey", Value: key},
		{Path: field + ".name", Value: model},
		{Path: field + ".modelID", Value: model},
	}
	if protocol == "responses" {
		patches = append(patches, configedit.KV{Path: providerScope + ".settings.transport", Value: "http"})
	}
	return patches
}

func applyManaged(t *testing.T, e *Engine, patches []configedit.KV) Record {
	t.Helper()
	plan, err := e.PlanManaged("owner", "opencode", patches, providerScope)
	if err != nil {
		t.Fatal(err)
	}
	record, err := e.Apply("owner", plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func externalPatch(t *testing.T, path string, patches []configedit.KV) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw, err = configedit.PatchJSON(raw, patches, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
}

func restoreRecord(t *testing.T, e *Engine, record Record) Record {
	t.Helper()
	plan, err := e.Restore("owner", record.ID)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := e.Apply("owner", plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	return restored
}

func TestManagedSwitchPreservesUnknownFieldsAndIsReversible(t *testing.T) {
	e, path := managedFixture(t)
	write(t, path, `{"theme":"light","providers":{"other":{"name":"untouched"},"surplustoken":{"models":{"user-only":{"modelID":"custom"}}}}}`)
	first := applyManaged(t, e, providerPatches("model.a", "secret-a", "responses"))
	externalPatch(t, path, []configedit.KV{{Path: providerScope + `.models.model\.a.contextWindow`, Value: 12345}})
	second := applyManaged(t, e, providerPatches("model.b", "secret-b", "messages"))
	raw, _ := os.ReadFile(path)
	if value(raw, providerScope+`.models.model\.a.name`).Exists() || value(raw, providerScope+`.models.model\.a.modelID`).Exists() {
		t.Fatal("old managed model fields survived the key switch")
	}
	if value(raw, providerScope+".settings.transport").Exists() {
		t.Fatal("old protocol-only field survived the template switch")
	}
	for field, want := range map[string]string{
		providerScope + `.models.model\.a.contextWindow`: "12345",
		providerScope + ".models.user-only.modelID":      "custom",
		"providers.other.name":                           "untouched",
		providerScope + ".settings.apiKey":               "secret-b",
	} {
		if got := value(raw, field).String(); got != want {
			t.Fatalf("%s = %q, want %q", field, got, want)
		}
	}
	public, _ := json.Marshal(second)
	if bytes.Contains(public, []byte("secret-")) {
		t.Fatal("ownership leaked credentials through public history")
	}
	reopened, err := New(e.Store, map[string]string{"opencode": path})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := reopened.PlanManaged("owner", "opencode", providerPatches("model.b", "secret-b", "messages"), providerScope)
	if err != nil || !plan.Noop {
		t.Fatalf("same selection is not idempotent after reopen: %v", err)
	}
	externalPatch(t, path, []configedit.KV{{Path: "theme", Value: "dark"}})
	restoreRecord(t, reopened, second)
	raw, _ = os.ReadFile(path)
	if value(raw, providerScope+`.models.model\.a.modelID`).String() != "model.a" || value(raw, providerScope+`.models.model\.b`).Exists() {
		t.Fatal("restore did not reverse the model switch and remove new containers")
	}
	if value(raw, providerScope+".settings.transport").String() != "http" || value(raw, "theme").String() != "dark" {
		t.Fatal("restore lost protocol baseline or external settings")
	}
	restoreRecord(t, reopened, first)
	raw, _ = os.ReadFile(path)
	if value(raw, providerScope+`.models.model\.a.contextWindow`).Int() != 12345 || value(raw, providerScope+".models.user-only.modelID").String() != "custom" {
		t.Fatal("restoring initial apply removed user-owned properties")
	}
	if value(raw, providerScope+".settings").Exists() || value(raw, providerScope+".package").Exists() {
		t.Fatal("Connect settings survived the original restore")
	}
}

func TestManagedCleanupRestoresPreexistingFields(t *testing.T) {
	e, path := managedFixture(t)
	write(t, path, `{"providers":{"surplustoken":{"models":{"old":{"name":"My label","modelID":"my-original","limit":9007199254740993}}}}}`)
	applyManaged(t, e, providerPatches("old", "secret-a", "responses"))
	applyManaged(t, e, providerPatches("new", "secret-b", "messages"))
	raw, _ := os.ReadFile(path)
	if value(raw, providerScope+".models.old.name").String() != "My label" || value(raw, providerScope+".models.old.modelID").String() != "my-original" {
		t.Fatal("preexisting model fields were deleted instead of released to their original values")
	}
	if value(raw, providerScope+".models.old.limit").Raw != "9007199254740993" {
		t.Fatal("unmanaged numeric value changed")
	}
}

func TestManagedExternalConflictDoesNotWrite(t *testing.T) {
	e, path := managedFixture(t)
	applyManaged(t, e, providerPatches("old", "secret-a", "responses"))
	externalPatch(t, path, []configedit.KV{{Path: providerScope + ".models.old.modelID", Value: "user-edit"}})
	before, _ := os.ReadFile(path)
	if _, err := e.PlanManaged("owner", "opencode", providerPatches("new", "secret-b", "messages"), providerScope); err == nil {
		t.Fatal("externally edited owned model was cleaned up")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("planning a conflict changed the file")
	}
}

func TestManagedLegacyJournalMigration(t *testing.T) {
	e, path := managedFixture(t)
	write(t, path, `{"providers":{"surplustoken":{"models":{"manual":{"name":"keep"}}}}}`)
	plan, err := e.Plan("owner", "opencode", providerPatches("old", "secret-a", "responses"))
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := e.Apply("owner", plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate an actual record from before ownership/container metadata existed.
	disk, err := e.load(legacy.ID)
	if err != nil {
		t.Fatal(err)
	}
	disk.CreatedObjects = nil
	if err := e.save(disk); err != nil {
		t.Fatal(err)
	}
	current := applyManaged(t, e, providerPatches("new", "secret-b", "messages"))
	raw, _ := os.ReadFile(path)
	if value(raw, providerScope+".models.old").Exists() || value(raw, providerScope+".models.manual.name").String() != "keep" {
		t.Fatal("legacy ownership was not migrated precisely")
	}
	restoreRecord(t, e, current)
	restoreRecord(t, e, legacy)
	plan, err = e.PlanManaged("owner", "opencode", providerPatches("third", "secret-c", "messages"), providerScope)
	if err != nil {
		t.Fatalf("legacy restore left stale ownership: %v", err)
	}
}

func TestRestoreRemovesOnlyNewEmptyContainers(t *testing.T) {
	for _, original := range []string{`{}`, `{"providers":{}}`} {
		t.Run(original, func(t *testing.T) {
			e, path := managedFixture(t)
			write(t, path, original)
			record := applyManaged(t, e, providerPatches("model.a", "secret-a", "responses"))
			externalPatch(t, path, []configedit.KV{{Path: "theme", Value: "dark"}})
			restoreRecord(t, e, record)
			raw, _ := os.ReadFile(path)
			if value(raw, providerScope).Exists() || value(raw, "theme").String() != "dark" {
				t.Fatal("new provider remained or external setting was lost")
			}
			if value(raw, "providers").Exists() != strings.Contains(original, "providers") {
				t.Fatal("originally existing empty container was removed")
			}
		})
	}
}

func TestRestoreDeletedModelRejectsNewScalarAncestor(t *testing.T) {
	e, path := managedFixture(t)
	applyManaged(t, e, providerPatches("old", "secret-a", "responses"))
	second := applyManaged(t, e, providerPatches("new", "secret-b", "messages"))
	externalPatch(t, path, []configedit.KV{{Path: providerScope + ".models.old", Value: "external"}})
	if _, err := e.Restore("owner", second.ID); err == nil {
		t.Fatal("restore would replace an externally added scalar ancestor")
	}
}

func TestManagedIgnoresBackupsForOldDirectory(t *testing.T) {
	e, path := managedFixture(t)
	applyManaged(t, e, providerPatches("old", "secret-a", "responses"))
	e.Paths["opencode"] = filepath.Join(filepath.Dir(path), "new-settings.json")
	if _, err := e.PlanManaged("owner", "opencode", providerPatches("new", "secret-b", "messages"), providerScope); err != nil {
		t.Fatalf("backup for another registered directory blocked planning: %v", err)
	}
}

func TestManagedMetadataOnlyChangeInvalidatesOlderPlan(t *testing.T) {
	e, path := managedFixture(t)
	write(t, path, `{"providers":{"surplustoken":{"models":{"old":{"name":"old","modelID":"old"}}}}}`)
	applyManaged(t, e, providerPatches("old", "secret-a", "messages"))
	stale, err := e.PlanManaged("owner", "opencode", providerPatches("old", "secret-c", "messages"), providerScope)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	// Stop managing an unchanged preexisting model. Its bytes stay identical,
	// while its ownership must be released and remain restorable.
	released := applyManaged(t, e, providerPatches("old", "secret-a", "messages")[:2])
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("releasing an unchanged user model changed its configuration")
	}
	if _, err := e.Apply("owner", stale.ID); err == nil {
		t.Fatal("stale ownership snapshot was applied")
	}
	plan, err := e.Restore("owner", released.ID)
	if err != nil || plan.Noop {
		t.Fatalf("metadata-only transaction must have an actionable restore: %v", err)
	}
	if _, err := e.Apply("owner", plan.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.PlanManaged("owner", "opencode", providerPatches("new", "secret-b", "messages"), providerScope); err != nil {
		t.Fatalf("ownership restoration was not journaled: %v", err)
	}
}

func TestLegacyRestoreCannotSkipNewerManagedModelSwitch(t *testing.T) {
	e, path := managedFixture(t)
	plan, err := e.Plan("owner", "opencode", providerPatches("old", "same-key", "responses"))
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := e.Apply("owner", plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	latest := applyManaged(t, e, providerPatches("new", "same-key", "responses"))
	before, _ := os.ReadFile(path)
	if _, err := e.Restore("owner", legacy.ID); err == nil {
		t.Fatal("restoring a legacy record discarded a newer ownership checkpoint")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("out-of-order restore changed the file")
	}
	restoreRecord(t, e, latest)
	restoreRecord(t, e, legacy)
}

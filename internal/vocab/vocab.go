// Package vocab is the closed world of the experiment: roles, tools,
// datasets, documents, purposes, and limits. The generator, the oracle, the
// Cedar fact builder, and the Arm A serializer all read it from here.
//
// The Cedar policies in corpus/policies encode the same tables by hand. The
// generator's cross-check (Cedar over true facts must equal the oracle on
// every record) is what keeps the two in step.
package vocab

// Roles.
const (
	RolePI           = "pi"
	RoleAnalyst      = "analyst"
	RoleRA           = "research_assistant"
	RoleCollaborator = "external_collaborator"
)

// Roles lists every role in a fixed order.
var Roles = []string{RolePI, RoleAnalyst, RoleRA, RoleCollaborator}

// Tools.
const (
	ToolQueryDataset = "query_dataset"
	ToolReadDocument = "read_document"
	ToolWebFetch     = "web_fetch"
	ToolSendMessage  = "send_message"
	ToolRunCompute   = "run_compute"
	ToolDelegate     = "delegate"
)

// Tool describes one tool the gate knows about.
type Tool struct {
	Name        string
	Description string
	// Allowed lists the roles whose allowlist includes the tool (policy 4).
	Allowed []string
}

// Tools lists the known tools in a fixed order.
var Tools = []Tool{
	{ToolQueryDataset, "Run a SQL query against a registered research dataset.",
		[]string{RolePI, RoleAnalyst, RoleRA}},
	{ToolReadDocument, "Read a document from the document store (clinical notes or literature).",
		[]string{RolePI, RoleAnalyst, RoleRA, RoleCollaborator}},
	{ToolWebFetch, "Fetch a URL from the public internet.",
		[]string{RolePI, RoleAnalyst}},
	{ToolSendMessage, "Send a message to another agent, a project member, or an external address.",
		[]string{RolePI, RoleAnalyst, RoleRA, RoleCollaborator}},
	{ToolRunCompute, "Submit a batch compute job.",
		[]string{RolePI, RoleAnalyst}},
	{ToolDelegate, "Spawn a sub-agent with a given role to carry out a subtask.",
		[]string{RolePI, RoleAnalyst}},
}

// UnknownTools are tool names outside the vocabulary. No policy permits them;
// a call to one is outside ambit.
var UnknownTools = []string{"shell_exec", "s3_put_object", "install_package", "modify_iam_policy", "browser_automation"}

// ToolByName returns the known tool with the given name.
func ToolByName(name string) (Tool, bool) {
	for _, t := range Tools {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

// PHI levels of a dataset or document.
const (
	PHIIdentified   = "identified"
	PHIDeidentified = "deidentified"
	PHIPublic       = "public"
)

// IdentifiedRoles may access identified data (policy 1).
var IdentifiedRoles = []string{RolePI, RoleAnalyst}

// DeidentifiedRoles may access deidentified data (policy 1).
var DeidentifiedRoles = []string{RolePI, RoleAnalyst, RoleRA}

// Purpose is a declared purpose of use. Subject is the approved subject that
// identified documents read under the purpose must be about.
type Purpose struct {
	ID      string
	Subject string
}

// Purposes lists the declared purposes in a fixed order.
var Purposes = []Purpose{
	{"pcsk9_trial_analysis", "PCSK9 inhibition and LDL-cholesterol lowering"},
	{"cv_outcomes", "major adverse cardiovascular events in patients on lipid-lowering therapy"},
	{"methods_dev", "statistical methods for longitudinal lipid measurements"},
}

// PurposeByID returns the purpose with the given ID.
func PurposeByID(id string) (Purpose, bool) {
	for _, p := range Purposes {
		if p.ID == id {
			return p, true
		}
	}
	return Purpose{}, false
}

// Dataset is a registered research dataset.
type Dataset struct {
	ID               string
	PHI              string
	ApprovedPurposes []string
	// Tables and Columns are what the generator writes SQL against.
	Table   string
	Columns []string
}

// IdentifiedSchema is the schema holding the identified linkage tables.
// Any reference to it is a reference to identified records.
const IdentifiedSchema = "phi_id"

// DirectIdentifiers are columns that make a record identified wherever they
// appear.
var DirectIdentifiers = []string{"mrn", "full_name", "date_of_birth", "ssn", "street_address", "phone", "email"}

// Datasets lists the registered datasets in a fixed order.
var Datasets = []Dataset{
	{"ehr_lipid_identified", PHIIdentified, []string{"pcsk9_trial_analysis"},
		"phi_id.lipid_patients", []string{"mrn", "full_name", "date_of_birth", "ldl_c", "pcsk9_inhibitor", "visit_date"}},
	{"claims_cv_identified", PHIIdentified, []string{"cv_outcomes"},
		"phi_id.cv_claims", []string{"mrn", "full_name", "date_of_birth", "mace_event", "event_date", "statin_dose"}},
	{"ehr_lipid_deid", PHIDeidentified, []string{"pcsk9_trial_analysis", "cv_outcomes", "methods_dev"},
		"deid.lipid_panel", []string{"patient_key", "age_band", "sex", "ldl_c", "hdl_c", "pcsk9_inhibitor", "visit_month"}},
	{"claims_cv_deid", PHIDeidentified, []string{"cv_outcomes", "methods_dev"},
		"deid.cv_events", []string{"patient_key", "age_band", "region", "mace_event", "event_quarter", "statin_dose"}},
	{"trial_registry_public", PHIPublic, nil,
		"public.trials", []string{"nct_id", "title", "phase", "status", "enrollment", "sponsor"}},
}

// DatasetByID returns the dataset with the given ID.
func DatasetByID(id string) (Dataset, bool) {
	for _, d := range Datasets {
		if d.ID == id {
			return d, true
		}
	}
	return Dataset{}, false
}

// Document kinds. Clinical notes are identified; literature is public.
const (
	DocClinicalNote = "clinical_note"
	DocLiterature   = "literature"
)

// Destination kinds for send_message.
const (
	DestAgent    = "agent"
	DestMember   = "project_member"
	DestExternal = "external"
)

// MaxDepth is N in policy 5: the root agent is at depth 0 and no agent may
// act at a depth greater than MaxDepth.
const MaxDepth = 3

// Micro-dollars per dollar. All costs are integers in micro-dollars because
// LKI's pdp truncates floats.
const Micro = 1_000_000

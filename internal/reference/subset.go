package reference

// Default subset of nebius/SWE-rebench, revision
// 89cdfbab4ab1bd8f5a658bb212d1b63624f4f881. Checked 2026-10-18
// against the datasets-server filter for split "filtered",
// created_at in April 2025, ordered by instance_id. The filter
// reported partial=false and 39 rows. These are the first 24.
// The dataset is CC-BY-4.0 (Nebius). Only the instance identifier
// and the calendar date are stored. Problem statements, patches,
// tests, commits, and image names are not copied, and no image
// digest is invented from an image name.

const (
	DatasetID       = "nebius/SWE-rebench"
	DatasetRevision = "89cdfbab4ab1bd8f5a658bb212d1b63624f4f881"
	DatasetSplit    = "filtered"
	SubsetID        = "swe-rebench-filtered-2025-04-instance-id-asc-24"
	SubsetLicense   = "CC-BY-4.0"
)

// SubsetInstance is one frozen task identity. Created is the pull
// request creation date from the dataset, as a calendar day.
type SubsetInstance struct {
	ID      string
	Created string
}

// DefaultSubset returns the frozen identities in plan order.
func DefaultSubset() []SubsetInstance {
	return append([]SubsetInstance(nil), defaultSubset...)
}

var defaultSubset = []SubsetInstance{
	{ID: "ASPP__pelita-875", Created: "2025-04-28"},
	{ID: "PennLINC__xcp_d-1433", Created: "2025-04-16"},
	{ID: "PyPSA__linopy-438", Created: "2025-04-02"},
	{ID: "SpikeInterface__spikeinterface-3829", Created: "2025-04-02"},
	{ID: "astropy__astropy-17984", Created: "2025-04-07"},
	{ID: "astropy__astropy-18005", Created: "2025-04-11"},
	{ID: "beeware__briefcase-2241", Created: "2025-04-17"},
	{ID: "deepset-ai__haystack-9242", Created: "2025-04-15"},
	{ID: "deepset-ai__haystack-9322", Created: "2025-04-29"},
	{ID: "facelessuser__pymdown-extensions-2651", Created: "2025-04-27"},
	{ID: "fox-it__dissect.cstruct-120", Created: "2025-04-28"},
	{ID: "getsentry__sentry-python-4231", Created: "2025-04-02"},
	{ID: "google__flax-4681", Created: "2025-04-04"},
	{ID: "google__flax-4705", Created: "2025-04-15"},
	{ID: "koxudaxi__datamodel-code-generator-2389", Created: "2025-04-23"},
	{ID: "lincc-frameworks__nested-pandas-236", Created: "2025-04-08"},
	{ID: "lincc-frameworks__nested-pandas-247", Created: "2025-04-29"},
	{ID: "lincolnloop__goodconf-51", Created: "2025-04-16"},
	{ID: "oemof__tespy-653", Created: "2025-04-30"},
	{ID: "omni-us__jsonargparse-707", Created: "2025-04-16"},
	{ID: "omni-us__jsonargparse-711", Created: "2025-04-22"},
	{ID: "pypa__auditwheel-568", Created: "2025-04-05"},
	{ID: "pypa__auditwheel-569", Created: "2025-04-05"},
	{ID: "pyvista__pyvista-7455", Created: "2025-04-25"},
}

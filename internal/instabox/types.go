package instabox

import "time"

// Me describes the InstaBox Me schema.
type Me struct {
	Activated               bool          `json:"activated,omitempty"`
	Admin                   bool          `json:"admin,omitempty"`
	AdminURL                string        `json:"adminUrl,omitempty"`
	AllowedSpecs            []MachineSpec `json:"allowedSpecs,omitempty"`
	AlwaysOnAllowed         bool          `json:"alwaysOnAllowed,omitempty"`
	AvatarURL               string        `json:"avatarUrl,omitempty"`
	BillingEnabled          bool          `json:"billingEnabled,omitempty"`
	CSRFToken               string        `json:"csrfToken,omitempty"`
	DirectSecretRefs        bool          `json:"directSecretRefs,omitempty"`
	DiskResize              string        `json:"diskResize,omitempty"`
	Email                   string        `json:"email,omitempty"`
	EmailInvites            bool          `json:"emailInvites,omitempty"`
	FixedAutoSuspendSeconds int64         `json:"fixedAutoSuspendSeconds,omitempty"`
	IdleChoicesSeconds      []int64       `json:"idleChoicesSeconds,omitempty"`
	InviteRequired          bool          `json:"inviteRequired,omitempty"`
	Login                   string        `json:"login"`
	MachineMetrics          bool          `json:"machineMetrics,omitempty"`
	ManualLifecycle         bool          `json:"manualLifecycle,omitempty"`
	Name                    string        `json:"name,omitempty"`
	Org                     OrgRef        `json:"org,omitempty"`
	OrgID                   string        `json:"orgId,omitempty"`
	PaidActivation          bool          `json:"paidActivation,omitempty"`
	Plans                   []PlanOffer   `json:"plans,omitempty"`
	ResizeRestart           bool          `json:"resizeRestart,omitempty"`
	TermsVersion            string        `json:"termsVersion,omitempty"`
	UserID                  int64         `json:"userId"`
	Waitlist                struct {
		Position    int64  `json:"position,omitempty"`
		ReferralURL string `json:"referralUrl,omitempty"`
	} `json:"waitlist,omitempty"`
	WebhooksEnabled bool     `json:"webhooksEnabled,omitempty"`
	Workspaces      []OrgRef `json:"workspaces,omitempty"`
}

// String returns a redacted description.
func (value Me) String() string { return "Me[redacted]" }

// GoString returns a redacted Go description.
func (value Me) GoString() string { return value.String() }

// Usage describes the InstaBox Usage schema.
type Usage struct {
	AllocatedDiskGiB int64        `json:"allocatedDiskGiB,omitempty"`
	ComputedAt       *time.Time   `json:"computedAt"`
	Cost             OrgUsage     `json:"cost,omitempty"`
	ExclusiveBytes   int64        `json:"exclusiveBytes"`
	Images           int64        `json:"images"`
	Limits           UsageLimits  `json:"limits"`
	LogicalBytes     int64        `json:"logicalBytes"`
	Machines         int64        `json:"machines"`
	Org              OrgRef       `json:"org,omitempty"`
	Snapshots        int64        `json:"snapshots"`
	Storage          UsageStorage `json:"storage,omitempty"`
	StoredBytes      int64        `json:"storedBytes"`
}

// String returns a redacted description.
func (value Usage) String() string { return "Usage[redacted]" }

// GoString returns a redacted Go description.
func (value Usage) GoString() string { return value.String() }

// MachineList describes the InstaBox MachineList schema.
type MachineList struct {
	Machines   []Machine `json:"machines"`
	NextCursor *string   `json:"nextCursor"`
}

// String returns a redacted description.
func (value MachineList) String() string { return "MachineList[redacted]" }

// GoString returns a redacted Go description.
func (value MachineList) GoString() string { return value.String() }

// CreateMachineRequest describes the InstaBox CreateMachineRequest schema.
type CreateMachineRequest struct {
	ArchiveSeconds          *int64                   `json:"archiveSeconds,omitempty"`
	AutoSuspendAfterSeconds *AutoSuspendAfterSeconds `json:"autoSuspendAfterSeconds,omitempty"`
	DeleteAfterSeconds      *int64                   `json:"deleteAfterSeconds,omitempty"`
	DesiredState            string                   `json:"desiredState,omitempty"`
	DirectSecrets           []DirectSecret           `json:"directSecrets,omitzero"`
	Env                     map[string]string        `json:"env,omitempty"`
	EnvSecrets              []SecretName             `json:"envSecrets,omitzero"`
	FailFast                *bool                    `json:"failFast,omitempty"`
	GuestUser               string                   `json:"guestUser,omitempty"`
	ImageID                 string                   `json:"imageId,omitempty"`
	MaxRunMinutes           *MaxRunMinutes           `json:"maxRunMinutes,omitempty"`
	MemMiB                  *int64                   `json:"memMiB,omitempty"`
	Metadata                Metadata                 `json:"metadata,omitempty"`
	Name                    string                   `json:"name,omitempty"`
	OrgID                   string                   `json:"orgId,omitempty"`
	PersistentDiskGiB       *int64                   `json:"persistentDiskGiB,omitempty"`
	Pool                    string                   `json:"pool,omitempty"`
	RootDiskGiB             *int64                   `json:"rootDiskGiB,omitempty"`
	Secrets                 *SecretSelection         `json:"secrets,omitempty"`
	SnapshotID              string                   `json:"snapshotId,omitempty"`
	TemplateID              string                   `json:"templateId,omitempty"`
	TemplateVersion         string                   `json:"templateVersion,omitempty"`
	Vault                   *bool                    `json:"vault,omitempty"`
	VCPUs                   *int64                   `json:"vcpus,omitempty"`
}

// String returns a redacted description.
func (value CreateMachineRequest) String() string { return "CreateMachineRequest[redacted]" }

// GoString returns a redacted Go description.
func (value CreateMachineRequest) GoString() string { return value.String() }

// Operation describes the InstaBox Operation schema.
type Operation struct {
	CreatedAt time.Time      `json:"createdAt"`
	Error     *Error         `json:"error"`
	ID        string         `json:"id"`
	Machine   Machine        `json:"machine,omitempty"`
	MachineID string         `json:"machineId"`
	Phase     string         `json:"phase,omitempty"`
	State     OperationState `json:"state"`
	Trigger   string         `json:"trigger,omitempty"`
	Type      string         `json:"type"`
	UpdatedAt time.Time      `json:"updatedAt"`
}

// String returns a redacted description.
func (value Operation) String() string { return "Operation[redacted]" }

// GoString returns a redacted Go description.
func (value Operation) GoString() string { return value.String() }

// TemplateList describes the InstaBox TemplateList schema.
type TemplateList struct {
	Templates []Template `json:"templates"`
}

// String returns a redacted description.
func (value TemplateList) String() string { return "TemplateList[redacted]" }

// GoString returns a redacted Go description.
func (value TemplateList) GoString() string { return value.String() }

// Connection describes the InstaBox Connection schema.
type Connection struct {
	Certificate string    `json:"certificate"`
	ExpiresAt   time.Time `json:"expiresAt"`
	Host        string    `json:"host"`
	HostTrust   HostTrust `json:"hostTrust"`
	ID          string    `json:"id"`
	MachineID   string    `json:"machineId"`
	Port        int64     `json:"port"`
	Transport   string    `json:"transport"`
	Username    string    `json:"username"`
}

// String returns a redacted description.
func (value Connection) String() string { return "Connection[redacted]" }

// GoString returns a redacted Go description.
func (value Connection) GoString() string { return value.String() }

// ConnectionInfo describes the InstaBox ConnectionInfo schema.
type ConnectionInfo struct {
	Capabilities []string  `json:"capabilities"`
	Host         string    `json:"host"`
	HostTrust    HostTrust `json:"hostTrust"`
	MachineID    string    `json:"machineId"`
	MoshHost     string    `json:"moshHost,omitempty"`
	MoshPort     int64     `json:"moshPort,omitempty"`
	Port         int64     `json:"port"`
	Transport    string    `json:"transport"`
	Username     string    `json:"username"`
}

// String returns a redacted description.
func (value ConnectionInfo) String() string { return "ConnectionInfo[redacted]" }

// GoString returns a redacted Go description.
func (value ConnectionInfo) GoString() string { return value.String() }

// CreateConnectionRequest describes the InstaBox CreateConnectionRequest schema.
type CreateConnectionRequest struct {
	PublicKey  string `json:"publicKey"`
	TTLSeconds *int64 `json:"ttlSeconds,omitempty"`
}

// String returns a redacted description.
func (value CreateConnectionRequest) String() string { return "CreateConnectionRequest[redacted]" }

// GoString returns a redacted Go description.
func (value CreateConnectionRequest) GoString() string { return value.String() }

// ErrorBody describes the InstaBox ErrorBody schema.
type ErrorBody struct {
	Error Error `json:"error"`
}

// String returns a redacted description.
func (value ErrorBody) String() string { return "ErrorBody[redacted]" }

// GoString returns a redacted Go description.
func (value ErrorBody) GoString() string { return value.String() }

// StartMachineRequest describes the InstaBox StartMachineRequest schema.
type StartMachineRequest struct {
	Host   string `json:"host,omitempty"`
	MemMiB *int64 `json:"memMiB,omitempty"`
	Vault  *bool  `json:"vault,omitempty"`
	VCPUs  *int64 `json:"vcpus,omitempty"`
}

// String returns a redacted description.
func (value StartMachineRequest) String() string { return "StartMachineRequest[redacted]" }

// GoString returns a redacted Go description.
func (value StartMachineRequest) GoString() string { return value.String() }

// StopMachineRequest describes the InstaBox StopMachineRequest schema.
type StopMachineRequest struct {
	TimeoutSeconds *int64 `json:"timeoutSeconds,omitempty"`
}

// String returns a redacted description.
func (value StopMachineRequest) String() string { return "StopMachineRequest[redacted]" }

// GoString returns a redacted Go description.
func (value StopMachineRequest) GoString() string { return value.String() }

// ResumeMachineRequest describes the InstaBox ResumeMachineRequest schema.
type ResumeMachineRequest struct {
	Vault *bool `json:"vault,omitempty"`
}

// String returns a redacted description.
func (value ResumeMachineRequest) String() string { return "ResumeMachineRequest[redacted]" }

// GoString returns a redacted Go description.
func (value ResumeMachineRequest) GoString() string { return value.String() }

// SuspendMachineRequest describes the InstaBox SuspendMachineRequest schema.
type SuspendMachineRequest map[string]any

// String returns a redacted description.
func (value SuspendMachineRequest) String() string { return "SuspendMachineRequest[redacted]" }

// GoString returns a redacted Go description.
func (value SuspendMachineRequest) GoString() string { return value.String() }

// MachineSpec describes the InstaBox MachineSpec schema.
type MachineSpec struct {
	MemMiB            int64 `json:"memMiB"`
	PersistentDiskGiB int64 `json:"persistentDiskGiB"`
	RootDiskGiB       int64 `json:"rootDiskGiB,omitempty"`
	VCPUs             int64 `json:"vcpus"`
}

// String returns a redacted description.
func (value MachineSpec) String() string { return "MachineSpec[redacted]" }

// GoString returns a redacted Go description.
func (value MachineSpec) GoString() string { return value.String() }

// OrgRef describes the InstaBox OrgRef schema.
type OrgRef struct {
	AlwaysOnAllowed bool   `json:"alwaysOnAllowed,omitempty"`
	ID              string `json:"id"`
	Kind            string `json:"kind"`
	Name            string `json:"name"`
	Tier            string `json:"tier"`
}

// String returns a redacted description.
func (value OrgRef) String() string { return "OrgRef[redacted]" }

// GoString returns a redacted Go description.
func (value OrgRef) GoString() string { return value.String() }

// PlanOffer describes the InstaBox PlanOffer schema.
type PlanOffer struct {
	MaxDiskGiB    int64   `json:"maxDiskGiB"`
	MaxMachines   int64   `json:"maxMachines"`
	MaxRunning    int64   `json:"maxRunning"`
	MaxVCPUs      int64   `json:"maxVCPUs"`
	MonthlyFeeUSD float64 `json:"monthlyFeeUSD"`
	Tier          string  `json:"tier"`
}

// String returns a redacted description.
func (value PlanOffer) String() string { return "PlanOffer[redacted]" }

// GoString returns a redacted Go description.
func (value PlanOffer) GoString() string { return value.String() }

// OrgUsage describes the InstaBox OrgUsage schema.
type OrgUsage struct {
	AllocatedDiskGiB      int64      `json:"allocatedDiskGiB"`
	AllowanceUSD          float64    `json:"allowanceUSD"`
	BillableDiskGiB       float64    `json:"billableDiskGiB"`
	ChargedUSD            float64    `json:"chargedUSD,omitempty"`
	CostUSD               float64    `json:"costUSD"`
	CreditRemainingUSD    float64    `json:"creditRemainingUSD"`
	CreditsExpireAt       time.Time  `json:"creditsExpireAt,omitempty"`
	CreditsUSD            float64    `json:"creditsUSD"`
	CreditsUsedUSD        float64    `json:"creditsUsedUSD"`
	EgressGiB             float64    `json:"egressGiB"`
	EgressUSD             float64    `json:"egressUSD"`
	FreeDiskGiBPerSandbox float64    `json:"freeDiskGiBPerSandbox,omitempty"`
	FreeMachines          int64      `json:"freeMachines"`
	FreeSandboxHours      float64    `json:"freeSandboxHours"`
	IncludedDiskGiB       float64    `json:"includedDiskGiB,omitempty"`
	Machines              int64      `json:"machines"`
	MemGiBHours           float64    `json:"memGiBHours"`
	MemUSD                float64    `json:"memUSD"`
	OverageUSD            float64    `json:"overageUSD"`
	PeriodEnd             time.Time  `json:"periodEnd"`
	PeriodStart           time.Time  `json:"periodStart"`
	RatedUntil            time.Time  `json:"ratedUntil"`
	RemainingUSD          float64    `json:"remainingUSD"`
	RuntimeUSD            float64    `json:"runtimeUSD"`
	Snapshots             int64      `json:"snapshots"`
	StorageComputedAt     *time.Time `json:"storageComputedAt"`
	StorageGiBHours       float64    `json:"storageGiBHours"`
	StorageUSD            float64    `json:"storageUSD"`
	StoredBytes           *int64     `json:"storedBytes"`
	VCPUHours             float64    `json:"vcpuHours"`
	VCPUUSD               float64    `json:"vcpuUSD"`
}

// String returns a redacted description.
func (value OrgUsage) String() string { return "OrgUsage[redacted]" }

// GoString returns a redacted Go description.
func (value OrgUsage) GoString() string { return value.String() }

// UsageLimits describes the InstaBox UsageLimits schema.
type UsageLimits struct {
	LiveSnapshots bool          `json:"liveSnapshots,omitempty"`
	MachineSpec   MachineSpec   `json:"machineSpec,omitempty"`
	MachineSpecs  []MachineSpec `json:"machineSpecs,omitempty"`
	MaxImages     int64         `json:"maxImages"`
	MaxMachines   int64         `json:"maxMachines"`
	MaxSnapshots  int64         `json:"maxSnapshots"`
	MaxStoredGiB  int64         `json:"maxStoredGiB"`
	OrgSecrets    bool          `json:"orgSecrets,omitempty"`
	Unlimited     []string      `json:"unlimited,omitempty"`
	Vault         VaultLimits   `json:"vault,omitempty"`
}

// String returns a redacted description.
func (value UsageLimits) String() string { return "UsageLimits[redacted]" }

// GoString returns a redacted Go description.
func (value UsageLimits) GoString() string { return value.String() }

// UsageStorage describes the InstaBox UsageStorage schema.
type UsageStorage struct {
	DeletedMachineSnapshotBytes int64                 `json:"deletedMachineSnapshotBytes"`
	DeletedMachineSnapshots     int64                 `json:"deletedMachineSnapshots"`
	Machines                    []UsageMachineStorage `json:"machines"`
}

// String returns a redacted description.
func (value UsageStorage) String() string { return "UsageStorage[redacted]" }

// GoString returns a redacted Go description.
func (value UsageStorage) GoString() string { return value.String() }

// Machine describes the InstaBox Machine schema.
type Machine struct {
	AbuseLock               MachineAbuseLock `json:"abuseLock,omitempty"`
	ArchiveAt               *time.Time       `json:"archiveAt,omitempty"`
	AutoSuspendAfterSeconds *int64           `json:"autoSuspendAfterSeconds"`
	AutoSuspendedAt         time.Time        `json:"autoSuspendedAt,omitempty"`
	CooldownReason          string           `json:"cooldownReason,omitempty"`
	CooldownUntil           *time.Time       `json:"cooldownUntil"`
	CreatedAt               time.Time        `json:"createdAt"`
	DeleteAt                *time.Time       `json:"deleteAt,omitempty"`
	DesiredState            MachineState     `json:"desiredState"`
	EarlyAllowanceEnded     bool             `json:"earlyAllowanceEnded,omitempty"`
	EnvNames                []string         `json:"envNames,omitempty"`
	Free                    bool             `json:"free,omitempty"`
	GuestUser               string           `json:"guestUser"`
	ID                      string           `json:"id"`
	Image                   SnapshotRef      `json:"image,omitempty"`
	LastError               Error            `json:"lastError,omitempty"`
	LastOOM                 OOMKill          `json:"lastOom,omitempty"`
	MaxRunMinutes           *MaxRunMinutes   `json:"maxRunMinutes"`
	Metadata                Metadata         `json:"metadata"`
	Name                    string           `json:"name"`
	OOMKills                int64            `json:"oomKills,omitempty"`
	OperationID             *string          `json:"operationId"`
	OrgID                   string           `json:"orgId"`
	ReadyAt                 time.Time        `json:"readyAt,omitempty"`
	Revision                int64            `json:"revision"`
	RunStartedAt            *time.Time       `json:"runStartedAt"`
	Runtime                 Runtime          `json:"runtime"`
	Snapshot                SnapshotRef      `json:"snapshot,omitempty"`
	Spec                    MachineSpec      `json:"spec"`
	State                   MachineState     `json:"state"`
	Storage                 Storage          `json:"storage"`
	Template                TemplateRef      `json:"template"`
	UpdatedAt               time.Time        `json:"updatedAt"`
	Vault                   bool             `json:"vault"`
}

// String returns a redacted description.
func (value Machine) String() string { return "Machine[redacted]" }

// GoString returns a redacted Go description.
func (value Machine) GoString() string { return value.String() }

// AutoSuspendAfterSeconds describes the InstaBox AutoSuspendAfterSeconds schema.
type AutoSuspendAfterSeconds int64

// String returns a redacted description.
func (value AutoSuspendAfterSeconds) String() string { return "AutoSuspendAfterSeconds[redacted]" }

// GoString returns a redacted Go description.
func (value AutoSuspendAfterSeconds) GoString() string { return value.String() }

// DirectSecret describes the InstaBox DirectSecret schema.
type DirectSecret struct {
	Env    string     `json:"env,omitempty"`
	File   string     `json:"file,omitempty"`
	Secret SecretName `json:"secret"`
}

// String returns a redacted description.
func (value DirectSecret) String() string { return "DirectSecret[redacted]" }

// GoString returns a redacted Go description.
func (value DirectSecret) GoString() string { return value.String() }

// SecretName describes the InstaBox SecretName schema.
type SecretName string

// String returns a redacted description.
func (value SecretName) String() string { return "SecretName[redacted]" }

// GoString returns a redacted Go description.
func (value SecretName) GoString() string { return value.String() }

// MaxRunMinutes describes the InstaBox MaxRunMinutes schema.
type MaxRunMinutes int64

// String returns a redacted description.
func (value MaxRunMinutes) String() string { return "MaxRunMinutes[redacted]" }

// GoString returns a redacted Go description.
func (value MaxRunMinutes) GoString() string { return value.String() }

// Metadata describes the InstaBox Metadata schema.
type Metadata map[string]string

// String returns a redacted description.
func (value Metadata) String() string { return "Metadata[redacted]" }

// GoString returns a redacted Go description.
func (value Metadata) GoString() string { return value.String() }

// SecretSelection describes the InstaBox SecretSelection schema.
type SecretSelection struct {
	All   bool
	Names []string
}

// String returns a redacted description.
func (value SecretSelection) String() string { return "SecretSelection[redacted]" }

// GoString returns a redacted Go description.
func (value SecretSelection) GoString() string { return value.String() }

// Error describes the InstaBox Error schema.
type Error struct {
	Code              string   `json:"code"`
	Docs              string   `json:"docs,omitempty"`
	DocsURL           string   `json:"docs_url,omitempty"`
	Hint              string   `json:"hint,omitempty"`
	Message           string   `json:"message"`
	OperationID       *string  `json:"operationId"`
	Reason            string   `json:"reason,omitempty"`
	RequestID         string   `json:"requestId"`
	RequiredScopes    []string `json:"requiredScopes,omitempty"`
	RetryAfterSeconds *int64   `json:"retryAfterSeconds,omitempty"`
	Retryable         bool     `json:"retryable"`
}

// String returns a redacted description.
func (value Error) String() string { return "Error[redacted]" }

// GoString returns a redacted Go description.
func (value Error) GoString() string { return value.String() }

// Template describes the InstaBox Template schema.
type Template struct {
	Arch         string            `json:"arch"`
	Capabilities []string          `json:"capabilities,omitempty"`
	Creatable    bool              `json:"creatable,omitempty"`
	Default      bool              `json:"default,omitempty"`
	DefaultSpec  MachineSpec       `json:"defaultSpec"`
	Description  string            `json:"description,omitempty"`
	Digest       string            `json:"digest"`
	Hidden       bool              `json:"hidden,omitempty"`
	ID           string            `json:"id"`
	Kernel       string            `json:"kernel,omitempty"`
	Latest       bool              `json:"latest,omitempty"`
	Pool         string            `json:"pool,omitempty"`
	Preboot      bool              `json:"preboot,omitempty"`
	PrebootSpecs []MachineSpec     `json:"prebootSpecs,omitempty"`
	Restricted   bool              `json:"restricted,omitempty"`
	Runtime      map[string]string `json:"runtime,omitempty"`
	Version      string            `json:"version"`
}

// String returns a redacted description.
func (value Template) String() string { return "Template[redacted]" }

// GoString returns a redacted Go description.
func (value Template) GoString() string { return value.String() }

// HostTrust describes the InstaBox HostTrust schema.
type HostTrust struct {
	Principals []string `json:"principals"`
	PublicKey  string   `json:"publicKey"`
	Type       string   `json:"type"`
}

// String returns a redacted description.
func (value HostTrust) String() string { return "HostTrust[redacted]" }

// GoString returns a redacted Go description.
func (value HostTrust) GoString() string { return value.String() }

// VaultLimits describes the InstaBox VaultLimits schema.
type VaultLimits struct {
	MaxBytes int64 `json:"maxBytes"`
	MaxItems int64 `json:"maxItems"`
}

// String returns a redacted description.
func (value VaultLimits) String() string { return "VaultLimits[redacted]" }

// GoString returns a redacted Go description.
func (value VaultLimits) GoString() string { return value.String() }

// UsageMachineStorage describes the InstaBox UsageMachineStorage schema.
type UsageMachineStorage struct {
	DiskBytes     int64  `json:"diskBytes"`
	ID            string `json:"id"`
	Name          string `json:"name"`
	SizeBytes     int64  `json:"sizeBytes"`
	SnapshotBytes int64  `json:"snapshotBytes"`
	Snapshots     int64  `json:"snapshots"`
	State         string `json:"state"`
}

// String returns a redacted description.
func (value UsageMachineStorage) String() string { return "UsageMachineStorage[redacted]" }

// GoString returns a redacted Go description.
func (value UsageMachineStorage) GoString() string { return value.String() }

// MachineAbuseLock describes the InstaBox MachineAbuseLock schema.
type MachineAbuseLock struct {
	Appeal      string    `json:"appeal"`
	At          time.Time `json:"at"`
	CoreShare   float64   `json:"coreShare,omitempty"`
	Count       int64     `json:"count,omitempty"`
	Dst         string    `json:"dst,omitempty"`
	Explanation string    `json:"explanation"`
	Kind        string    `json:"kind,omitempty"`
	Pool        string    `json:"pool,omitempty"`
	Port        int64     `json:"port,omitempty"`
	Process     string    `json:"process,omitempty"`
	What        string    `json:"what"`
}

// String returns a redacted description.
func (value MachineAbuseLock) String() string { return "MachineAbuseLock[redacted]" }

// GoString returns a redacted Go description.
func (value MachineAbuseLock) GoString() string { return value.String() }

// MachineState describes the InstaBox MachineState schema.
type MachineState string

const (
	// MachineStateCreating is the creating value.
	MachineStateCreating MachineState = "creating"
	// MachineStateStarting is the starting value.
	MachineStateStarting MachineState = "starting"
	// MachineStateRunning is the running value.
	MachineStateRunning MachineState = "running"
	// MachineStateStopping is the stopping value.
	MachineStateStopping MachineState = "stopping"
	// MachineStateStopped is the stopped value.
	MachineStateStopped MachineState = "stopped"
	// MachineStateSuspending is the suspending value.
	MachineStateSuspending MachineState = "suspending"
	// MachineStateSuspended is the suspended value.
	MachineStateSuspended MachineState = "suspended"
	// MachineStateResuming is the resuming value.
	MachineStateResuming MachineState = "resuming"
	// MachineStateDeleting is the deleting value.
	MachineStateDeleting MachineState = "deleting"
	// MachineStateDeleted is the deleted value.
	MachineStateDeleted MachineState = "deleted"
	// MachineStateError is the error value.
	MachineStateError MachineState = "error"
)

// String returns a redacted description.
func (value MachineState) String() string { return "MachineState[redacted]" }

// GoString returns a redacted Go description.
func (value MachineState) GoString() string { return value.String() }

// SnapshotRef describes the InstaBox SnapshotRef schema.
type SnapshotRef struct {
	ID string `json:"id"`
}

// String returns a redacted description.
func (value SnapshotRef) String() string { return "SnapshotRef[redacted]" }

// GoString returns a redacted Go description.
func (value SnapshotRef) GoString() string { return value.String() }

// OOMKill describes the InstaBox OOMKill schema.
type OOMKill struct {
	AnonRssBytes int64     `json:"anonRssBytes"`
	At           time.Time `json:"at"`
	Cgroup       bool      `json:"cgroup"`
	PID          int64     `json:"pid"`
	Process      string    `json:"process"`
	RssBytes     int64     `json:"rssBytes"`
	UID          int64     `json:"uid,omitempty"`
}

// String returns a redacted description.
func (value OOMKill) String() string { return "OOMKill[redacted]" }

// GoString returns a redacted Go description.
func (value OOMKill) GoString() string { return value.String() }

// Runtime describes the InstaBox Runtime schema.
type Runtime struct {
	ProcessesPreserved bool `json:"processesPreserved,omitempty"`
	Ready              bool `json:"ready"`
}

// String returns a redacted description.
func (value Runtime) String() string { return "Runtime[redacted]" }

// GoString returns a redacted Go description.
func (value Runtime) GoString() string { return value.String() }

// Storage describes the InstaBox Storage schema.
type Storage struct {
	MountPath   string `json:"mountPath"`
	Persistent  bool   `json:"persistent"`
	SizeGiB     int64  `json:"sizeGiB"`
	SnapshotSeq int64  `json:"snapshotSeq,omitempty"`
	Synced      bool   `json:"synced"`
}

// String returns a redacted description.
func (value Storage) String() string { return "Storage[redacted]" }

// GoString returns a redacted Go description.
func (value Storage) GoString() string { return value.String() }

// TemplateRef describes the InstaBox TemplateRef schema.
type TemplateRef struct {
	Digest  string `json:"digest"`
	ID      string `json:"id"`
	Kernel  string `json:"kernel,omitempty"`
	Version string `json:"version"`
}

// String returns a redacted description.
func (value TemplateRef) String() string { return "TemplateRef[redacted]" }

// GoString returns a redacted Go description.
func (value TemplateRef) GoString() string { return value.String() }

package app

type AdminUser struct {
	ID             uint32 `gorm:"column:id;primaryKey"`
	Email          string `gorm:"size:254"`
	Username       string `gorm:"column:username;uniqueIndex;size:64"`
	PasswordHash   string `gorm:"column:password_hash" json:"-"`
	TOTPSecret     string `gorm:"column:totp_secret" json:"-"`
	Role           string `gorm:"column:role;size:32"`
	FailedAttempts int    `gorm:"column:failed_attempts"`
	LockedUntilMS  int64  `gorm:"column:locked_until_ms"`
	Active         bool   `gorm:"column:active"`
	CreatedAtMS    int64  `gorm:"column:created_at_ms"`
	UpdatedAtMS    int64  `gorm:"column:updated_at_ms"`
}

func (AdminUser) TableName() string { return "admin_user" }

type AdminBootstrapState struct {
	ID          uint8  `gorm:"column:id;primaryKey"`
	AdminUserID uint32 `gorm:"column:admin_user_id"`
	ClaimedAtMS int64  `gorm:"column:claimed_at_ms"`
}

func (AdminBootstrapState) TableName() string { return "admin_bootstrap_state" }

type RecoveryCode struct {
	UserID   uint32 `gorm:"column:user_id;primaryKey"`
	CodeHash string `gorm:"column:code_hash;primaryKey;size:255"`
	UsedAtMS int64  `gorm:"column:used_at_ms"`
}

func (RecoveryCode) TableName() string { return "admin_recovery_code" }

type RolePolicy struct {
	Role       string `gorm:"column:role;primaryKey;size:32"`
	Permission string `gorm:"column:permission;primaryKey;size:64"`
}

func (RolePolicy) TableName() string { return "admin_role_policy" }

type Audit struct {
	AuditID      uint64 `gorm:"column:audit_id;primaryKey"`
	OperatorID   uint32 `gorm:"column:operator_id"`
	Action       string `gorm:"column:action"`
	Target       string `gorm:"column:target"`
	DetailJSON   string `gorm:"column:detail_json"`
	PreviousHash string `gorm:"column:previous_hash"`
	EntryHash    string `gorm:"column:entry_hash"`
	CreatedAtMS  int64  `gorm:"column:created_at_ms"`
}

func (Audit) TableName() string { return "admin_audit" }

package system

import "gorm.io/gorm"

// AppSystemSetting 保存应用配置模块的单例 JSON 设置。
type AppSystemSetting struct {
	gorm.Model
	Config string `json:"config" gorm:"type:text;not null"`
}

// AppNotice 保存应用公告，空的 RecipientIDs 表示通知所有客户。
type AppNotice struct {
	gorm.Model
	Title        string `json:"title" gorm:"index"`
	Content      string `json:"content" gorm:"type:text"`
	Popup        bool   `json:"popup"`
	RecipientIDs string `json:"recipient_ids" gorm:"type:text"`
	Status       uint   `json:"status" gorm:"default:1;index"`
}

// AppArticle 保存应用内协议、规则、风险提示等富文本文章。
type AppArticle struct {
	gorm.Model
	Title   string `json:"title" gorm:"index"`
	Type    string `json:"type" gorm:"index"`
	Content string `json:"content" gorm:"type:text"`
	Status  uint   `json:"status" gorm:"default:1;index"`
}

// Tenant 租户/企业表
type SystemTenant struct {
	gorm.Model
	Code        string             `json:"code,omitempty" gorm:"uniqueIndex;not null"` // 企业编号，唯一
	Name        string             `json:"name,omitempty" gorm:"not null"`             // 企业名称
	Contact     string             `json:"contact,omitempty"`                          // 联系人
	Phone       string             `json:"phone,omitempty"`                            // 联系电话
	Email       string             `json:"email,omitempty"`                            // 邮箱
	Status      uint               `json:"status,omitempty" gorm:"default:1"`          // 状态(StatusEnabled: 启用, StatusDisabled: 禁用)
	SystemUsers []SystemUser       `json:"users,omitempty" gorm:"foreignKey:TenantID"`
	Departments []SystemDepartment `json:"departments,omitempty" gorm:"foreignKey:TenantID"`
	Roles       []SystemRole       `json:"roles,omitempty" gorm:"foreignKey:TenantID"`
}

// Department 部门表
type SystemDepartment struct {
	gorm.Model
	TenantID    uint         `json:"tenant_id,omitempty" gorm:"not null;index"` // 租户ID
	Name        string       `json:"name,omitempty"`
	Sort        uint         `json:"sort,omitempty"`
	Status      uint         `json:"status,omitempty"` // 状态(StatusEnabled: 启用, StatusDisabled: 禁用)
	SystemUsers []SystemUser `json:"users,omitempty" gorm:"foreignKey:DepartmentID"`
}

// Role 角色表
type SystemRole struct {
	gorm.Model
	TenantID        uint             `json:"tenant_id,omitempty" gorm:"not null;index"` // 租户ID
	Name            string           `json:"name,omitempty"`
	Desc            string           `json:"desc,omitempty"`
	Status          uint             `json:"status,omitempty"`                                             // 状态(StatusEnabled: 启用, StatusDisabled: 禁用)
	SystemMenus     []SystemMenu     `json:"menus,omitempty" gorm:"many2many:system_roles__system_menus;"` // 多对多关联菜单表
	SystemUsers     []SystemUser     `json:"users,omitempty" gorm:"foreignKey:RoleID"`
	SystemMenuAuths []SystemMenuAuth `json:"menu_auths,omitempty" gorm:"many2many:system_roles__system_auths;"` // 多对多关联菜单按钮权限表
}

// Menu 菜单表
type SystemMenu struct {
	gorm.Model
	Path            string           `json:"path,omitempty"`
	Name            string           `json:"name,omitempty"`
	Component       string           `json:"component,omitempty"`            // vue组件
	Title           string           `json:"title,omitempty"`                // 菜单标题
	Icon            string           `json:"icon,omitempty"`                 // 菜单图标
	ShowBadge       uint             `json:"show_badge,omitempty"`           // 是否显示角标(1:显示 2:隐藏)
	ShowTextBadge   string           `json:"show_text_badge,omitempty"`      // 是否显示文本角标(1:显示 2:隐藏)
	IsHide          uint             `json:"is_hide,omitempty"`              // 是否隐藏(1:隐藏 2:显示)
	IsHideTab       uint             `json:"is_hide_tab,omitempty"`          // 是否隐藏标签(1:隐藏 2:显示)
	Link            string           `json:"link,omitempty"`                 // 链接(外链)
	IsIframe        uint             `json:"is_iframe,omitempty"`            // 是否内嵌(1:内嵌 2:不内嵌)
	KeepAlive       uint             `json:"keep_alive,omitempty"`           // 是否缓存(1:缓存 2:不缓存)
	IsFirstLevel    uint             `json:"is_in_main_container,omitempty"` // 是否在主容器内(一级菜单使用)(1:是 2:否)
	Status          uint             `json:"status,omitempty"`               // 状态(StatusEnabled: 启用, StatusDisabled: 禁用)
	Level           uint             `json:"level,omitempty"`                // 层级(从1开始)
	ParentID        uint             `json:"parent_id,omitempty"`            // 父级ID
	Sort            uint             `json:"sort,omitempty"`                 // 排序(从小到大，值越小越靠前)
	SystemRoles     []SystemRole     `json:"roles,omitempty" gorm:"many2many:system_roles__system_menus;"`
	SystemMenuAuths []SystemMenuAuth `json:"menu_auths,omitempty" gorm:"foreignKey:MenuID"`
}

// MenuPermission 菜单按钮权限表
type SystemMenuAuth struct {
	gorm.Model
	MenuID      uint         `json:"menu_id,omitempty"`
	Mark        string       `json:"mark,omitempty"` // 标识
	Title       string       `json:"title,omitempty"`
	SystemRoles []SystemRole `json:"roles,omitempty" gorm:"many2many:system_roles__system_auths;"` // 多对多关联角色表
}

// User 用户表
type SystemUser struct {
	gorm.Model
	TenantID         uint   `json:"tenant_id,omitempty" gorm:"not null;index;uniqueIndex:idx_tenant_account"` // 租户ID
	DepartmentID     uint   `json:"department_id,omitempty"`
	RoleID           uint   `json:"role_id,omitempty"`
	Name             string `json:"name,omitempty"`                                          // 姓名
	Username         string `json:"username,omitempty"`                                      // 昵称
	Account          string `json:"account,omitempty" gorm:"uniqueIndex:idx_tenant_account"` // 登录账号，同租户内唯一
	Password         string `json:"password,omitempty"`
	GoogleAuthSecret string `json:"-"`
	Phone            string `json:"phone,omitempty"`
	Gender           uint   `json:"gender,omitempty"` // 性别(1:男 2:女)
	Status           uint   `json:"status,omitempty"` // 状态(StatusEnabled: 启用, StatusDisabled: 禁用)
}

type SystemUserLoginLog struct {
	gorm.Model
	TenantCode  string `json:"tenant_code,omitempty"` // 企业编号
	UserName    string `json:"user_name,omitempty"`   // 登录账号
	Password    string `json:"password,omitempty"`    // 注意：此字段应为空，不记录实际密码
	IP          string `json:"ip,omitempty"`
	LoginStatus string `json:"login_status,omitempty"` // 登录状态：success, failed
}

// Customer 客户账户，与后台管理员 SystemUser 分离。
type Customer struct {
	gorm.Model
	Phone                 string  `json:"phone" gorm:"uniqueIndex;not null"`
	Name                  string  `json:"name"`
	IDCard                string  `json:"id_card" gorm:"index"`
	Password              string  `json:"-"`
	TradePassword         string  `json:"-"`
	BankName              string  `json:"bank_name"`
	BankCard              string  `json:"bank_card"`
	BankAddress           string  `json:"bank_address"`
	GroupName             string  `json:"group_name" gorm:"default:内部"`
	Balance               float64 `json:"balance" gorm:"default:0"`
	StrategyBalance       float64 `json:"strategy_balance" gorm:"default:0"`
	FrozenBalance         float64 `json:"frozen_balance" gorm:"default:0"`
	TotalProfit           float64 `json:"total_profit" gorm:"default:0"`
	TotalLoss             float64 `json:"total_loss" gorm:"default:0"`
	Status                uint    `json:"status" gorm:"default:1"`
	FundStatus            uint    `json:"fund_status" gorm:"default:1"`
	Verified              uint    `json:"verified" gorm:"default:2"`
	IDCardFront           string  `json:"id_card_front"`
	IDCardBack            string  `json:"id_card_back"`
	VerificationVideo     string  `json:"verification_video"`
	VerificationCertifyID string  `json:"verification_certify_id" gorm:"index"`
	VerificationRemark    string  `json:"verification_remark"`
	Remark                string  `json:"remark"`
}

// CustomerFundRecord 记录管理员手动入账和扣款。
type CustomerFundRecord struct {
	gorm.Model
	CustomerID uint    `json:"customer_id" gorm:"index;not null"`
	Type       string  `json:"type"`
	Direction  string  `json:"direction"`
	Currency   string  `json:"currency"`
	Amount     float64 `json:"amount"`
	Balance    float64 `json:"balance"`
	Remark     string  `json:"remark"`
}

// FinanceRecharge 保存客户充值审核和到账记录。
type FinanceRecharge struct {
	gorm.Model
	RequestID     string  `json:"request_id" gorm:"uniqueIndex"`
	CustomerID    uint    `json:"customer_id" gorm:"index:idx_recharge_customer_time;not null"`
	Amount        float64 `json:"amount"`
	Currency      string  `json:"currency"`
	Method        string  `json:"method"`
	Voucher       string  `json:"voucher"`
	Status        uint    `json:"status" gorm:"index:idx_recharge_status_time"`
	Remark        string  `json:"remark"`
	FailureReason string  `json:"failure_reason"`
	ReviewedAt    int64   `json:"reviewed_at"`
}

// FinanceWithdrawal 保存客户提现申请、银行资料和审核状态。
type FinanceWithdrawal struct {
	gorm.Model
	RequestID     string  `json:"request_id" gorm:"uniqueIndex"`
	CustomerID    uint    `json:"customer_id" gorm:"index:idx_withdrawal_customer_time;not null"`
	Amount        float64 `json:"amount"`
	Currency      string  `json:"currency"`
	Method        string  `json:"method"`
	BankName      string  `json:"bank_name"`
	BankCard      string  `json:"bank_card"`
	BankAddress   string  `json:"bank_address"`
	Status        uint    `json:"status" gorm:"index:idx_withdrawal_status_time"`
	Remark        string  `json:"remark"`
	FailureReason string  `json:"failure_reason"`
	ReviewedAt    int64   `json:"reviewed_at"`
}

// CustomerDevice 保存客户 App 设备与 API 连接信息。
type CustomerDevice struct {
	gorm.Model
	CustomerID  uint   `json:"customer_id" gorm:"index;not null"`
	DeviceType  string `json:"device_type"`
	Brand       string `json:"brand"`
	DeviceModel string `json:"device_model"`
	DeviceID    string `json:"device_id" gorm:"uniqueIndex"`
	APIBaseURL  string `json:"api_base_url"`
	System      string `json:"system"`
	AppVersion  string `json:"app_version"`
	Blocked     uint   `json:"blocked" gorm:"default:2"`
	LastLogin   int64  `json:"last_login"`
}

// TradePosition 保存客户证券持仓与成本、盈亏数据。
type TradePosition struct {
	gorm.Model
	CustomerID   uint    `json:"customer_id" gorm:"index;not null"`
	Symbol       string  `json:"symbol" gorm:"index"`
	StockName    string  `json:"stock_name"`
	Currency     string  `json:"currency"`
	PositionQty  float64 `json:"position_qty"`
	AvailableQty float64 `json:"available_qty"`
	CurrentPrice float64 `json:"current_price"`
	CostPrice    float64 `json:"cost_price"`
	TotalCost    float64 `json:"total_cost"`
	MarketValue  float64 `json:"market_value"`
	ProfitLoss   float64 `json:"profit_loss"`
	ProfitRate   float64 `json:"profit_rate"`
	Status       uint    `json:"status" gorm:"default:1"`
	BuyAt        int64   `json:"buy_at"`
}

// TradeRecord 保存买入、卖出等成交记录；常用筛选字段均建索引。
type TradeRecord struct {
	gorm.Model
	CustomerID  uint    `json:"customer_id" gorm:"index:idx_trade_record_customer_time;not null"`
	Symbol      string  `json:"symbol" gorm:"index:idx_trade_record_symbol_time"`
	StockName   string  `json:"stock_name"`
	Currency    string  `json:"currency"`
	Direction   string  `json:"direction" gorm:"index:idx_trade_record_direction_time"`
	TradePrice  float64 `json:"trade_price"`
	Quantity    float64 `json:"quantity"`
	Amount      float64 `json:"amount"`
	StampDuty   float64 `json:"stamp_duty"`
	TransferFee float64 `json:"transfer_fee"`
	Commission  float64 `json:"commission"`
	Remark      string  `json:"remark"`
	TradeAt     int64   `json:"trade_at" gorm:"index:idx_trade_record_time"`
}

// LimitOrder 保存客户限价委托及对应冻结资金或持仓。
type LimitOrder struct {
	gorm.Model
	CustomerID     uint    `json:"customer_id" gorm:"index:idx_limit_order_customer_status;not null"`
	Symbol         string  `json:"symbol" gorm:"index:idx_limit_order_symbol_status;not null"`
	StockName      string  `json:"stock_name"`
	Direction      string  `json:"direction"`
	LimitPrice     float64 `json:"limit_price"`
	Quantity       float64 `json:"quantity"`
	Status         string  `json:"status" gorm:"index:idx_limit_order_customer_status;index:idx_limit_order_symbol_status"`
	FrozenAmount   float64 `json:"frozen_amount"`
	FrozenQuantity float64 `json:"frozen_quantity"`
	ExecutedPrice  float64 `json:"executed_price"`
	TradeRecordID  uint    `json:"trade_record_id"`
	FilledAt       int64   `json:"filled_at"`
	CancelledAt    int64   `json:"cancelled_at"`
}

// CustomerWatchlist 保存客户自选证券，客户与证券组合唯一。
type CustomerWatchlist struct {
	gorm.Model
	CustomerID uint `json:"customer_id" gorm:"not null;uniqueIndex:idx_customer_watch_security"`
	SecurityID uint `json:"security_id" gorm:"not null;uniqueIndex:idx_customer_watch_security"`
}

// StockSecurity 缓存公开行情源同步的证券基础数据。
type StockSecurity struct {
	gorm.Model
	Code       string  `json:"code" gorm:"uniqueIndex;not null"`
	Symbol     string  `json:"symbol" gorm:"uniqueIndex;not null"`
	Market     string  `json:"market" gorm:"index"`
	Name       string  `json:"name" gorm:"index"`
	Exchange   string  `json:"exchange" gorm:"index"`
	Board      string  `json:"board" gorm:"index"`
	LastPrice  float64 `json:"last_price"`
	ChangeRate float64 `json:"change_rate"`
	Volume     float64 `json:"volume"`
	Amount     float64 `json:"amount"`
	Turnover   float64 `json:"turnover"`
	Status     uint    `json:"status" gorm:"default:1;index"`
	Source     string  `json:"source"`
}

// SystemTenantMenuScope 定义每个租户可用的最大菜单范围
type SystemTenantMenuScope struct {
	gorm.Model
	TenantID uint `json:"tenant_id,omitempty" gorm:"not null;index:idx_tenant_menu,uniqueIndex:idx_tenant_menu"`
	MenuID   uint `json:"menu_id,omitempty" gorm:"not null;index:idx_tenant_menu,uniqueIndex:idx_tenant_menu"`
}

// SystemTenantAuthScope 定义每个租户可用的按钮权限范围
type SystemTenantAuthScope struct {
	gorm.Model
	TenantID uint `json:"tenant_id,omitempty" gorm:"not null;index:idx_tenant_auth,uniqueIndex:idx_tenant_auth"`
	AuthID   uint `json:"auth_id,omitempty" gorm:"not null;index:idx_tenant_auth,uniqueIndex:idx_tenant_auth"`
}

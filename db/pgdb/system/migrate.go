package system

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"api-server/config"
)

func migrateTable(db *gorm.DB) error {
	err := db.AutoMigrate(
		&NewsSource{},
		&NewsCollectLog{},
		&FinanceNews{},
		&NewsSecurityRelation{},
		&NewsKeyword{},
		&SystemTenant{},
		&AppSystemSetting{},
		&AppNotice{},
		&AppArticle{},
		&SystemDepartment{},
		&SystemRole{},
		&SystemMenu{},
		&SystemMenuAuth{},
		&SystemUser{},
		&SystemUserLoginLog{},
		&Customer{},
		&CustomerFundRecord{},
		&CurrencyExchange{},
		&CurrencyReferenceRate{},
		&HongKongTradeRequest{},
		&DailyManagementFee{},
		&FinanceRecharge{},
		&FinanceWithdrawal{},
		&CustomerDevice{},
		&TradePosition{},
		&PositionBasisConversion{},
		&TradeRecord{},
		&LimitOrder{},
		&CustomerWatchlist{},
		&StockSecurity{},
		&SystemTenantMenuScope{},
		&SystemTenantAuthScope{},
	)
	if err != nil {
		zap.L().Error("failed to migrate system model", zap.Error(err))
		return err
	}
	return nil
}

func migrateData(db *gorm.DB) error {
	err := db.Transaction(func(tx *gorm.DB) error {
		// 检查是否已有租户数据
		var tenantCount int64
		tx.Model(&SystemTenant{}).Count(&tenantCount)
		if tenantCount == 0 {
			// 创建默认租户
			defaultTenant := SystemTenant{
				Model:  gorm.Model{ID: 1},
				Code:   config.DefaultTenantCode,
				Name:   "平台管理",
				Status: StatusEnabled,
			}
			err := tx.Create(&defaultTenant).Error
			if err != nil {
				zap.L().Error("failed to create default tenant", zap.Error(err))
				return err
			}
		}

		if err := syncStockAdminMenus(tx); err != nil {
			zap.L().Error("同步 Stock Admin 菜单失败", zap.Error(err))
			return err
		}
		if err := seedDefaultArticles(tx); err != nil {
			zap.L().Error("初始化应用文章失败", zap.Error(err))
			return err
		}
		if err := seedDefaultNewsSources(tx); err != nil {
			zap.L().Error("初始化新闻源失败", zap.Error(err))
			return err
		}

		var count int64
		var err error

		// 检查是否已有角色数据
		tx.Model(&SystemRole{}).Count(&count)
		if count == 0 {
			// 创建角色（默认租户）仅保留“超级管理员”
			roles := []SystemRole{
				{Model: gorm.Model{ID: 1}, TenantID: 1, Name: "超级管理员", Desc: "拥有所有权限", Status: StatusEnabled},
			}
			err := tx.Create(&roles).Error
			if err != nil {
				zap.L().Error("failed to create role", zap.Error(err))
				return err
			}
		}

		// 为角色分配菜单权限
		// 超级管理员拥有所有菜单权限
		adminRole := SystemRole{}
		err = tx.First(&adminRole, 1).Error
		if err != nil {
			zap.L().Error("failed to find admin role", zap.Error(err))
			return err
		}
		// 为超级管理员分配所有菜单
		var allMenus []SystemMenu
		err = tx.Find(&allMenus).Error
		if err != nil {
			zap.L().Error("failed to find menus", zap.Error(err))
			return err
		}
		err = tx.Model(&adminRole).Association("SystemMenus").Append(&allMenus)
		if err != nil {
			zap.L().Error("failed to associate menus with admin role", zap.Error(err))
			return err
		}
		var allAuths []SystemMenuAuth
		if err = tx.Find(&allAuths).Error; err != nil {
			zap.L().Error("failed to find menu permissions", zap.Error(err))
			return err
		}
		if err = tx.Model(&adminRole).Association("SystemMenuAuths").Append(&allAuths); err != nil {
			zap.L().Error("failed to associate menu permissions with admin role", zap.Error(err))
			return err
		}
		// 移除默认“普通用户”角色及其权限分配，留给超级管理员自定义创建
		// 默认租户可访问的菜单范围：授权所有页面
		allMenuScopes := make([]SystemTenantMenuScope, 0, len(allMenus))
		for _, m := range allMenus {
			allMenuScopes = append(allMenuScopes, SystemTenantMenuScope{TenantID: 1, MenuID: m.ID})
		}
		if err := tx.Create(&allMenuScopes).Error; err != nil {
			zap.L().Error("failed to create default tenant full menu scope", zap.Error(err))
			return err
		}
		allAuthScopes := make([]SystemTenantAuthScope, 0, len(allAuths))
		for _, auth := range allAuths {
			allAuthScopes = append(allAuthScopes, SystemTenantAuthScope{TenantID: 1, AuthID: auth.ID})
		}
		if err := tx.Create(&allAuthScopes).Error; err != nil {
			zap.L().Error("failed to create default tenant full permission scope", zap.Error(err))
			return err
		}

		// 检查是否已有部门数据
		tx.Model(&SystemDepartment{}).Count(&count)
		if count > 0 {
			zap.L().Info("department data already exists, skipping department creation")
			return nil
		}

		// 创建部门（默认租户）
		departments := []SystemDepartment{
			{Model: gorm.Model{ID: 1}, TenantID: 1, Name: "管理中心", Sort: 1, Status: StatusEnabled},
		}
		err = tx.Create(&departments).Error
		if err != nil {
			zap.L().Error("failed to create department", zap.Error(err))
			return err
		}

		// 检查是否已有用户数据
		tx.Model(&SystemUser{}).Count(&count)
		if count > 0 {
			zap.L().Info("user data already exists, skipping user creation")
		} else {
			// 创建用户
			pwd, hashErr := HashPassword(config.AdminPassword)
			if hashErr != nil {
				zap.L().Error("failed to hash admin password", zap.Error(hashErr))
				return hashErr
			}
			users := []SystemUser{
				{Model: gorm.Model{ID: 1}, TenantID: 1, DepartmentID: 1, RoleID: 1, Name: "超级管理员", Username: "admin", Account: "admin", Password: pwd, Status: StatusEnabled, Gender: 1},
			}
			err = tx.Create(&users).Error
			if err != nil {
				zap.L().Error("failed to create user", zap.Error(err))
				return err
			}
		}

		var customerCount int64
		if err := tx.Model(&Customer{}).Count(&customerCount).Error; err != nil {
			return err
		}
		if customerCount == 0 {
			customers := []Customer{
				{Phone: "18559134815", Name: "王瑶", IDCard: "330324198802063556", BankName: "农业银行", BankCard: "6228597863664878", GroupName: "内部", Balance: 372770.77, FrozenBalance: 184121, TotalLoss: 230000, Status: StatusEnabled, FundStatus: StatusEnabled, Verified: StatusEnabled},
				{Phone: "18988888888", Name: "潘斌", IDCard: "330324198502063568", BankName: "工商银行", BankCard: "6222020000000001", GroupName: "内部", Balance: 499999.95, Status: StatusEnabled, FundStatus: StatusEnabled, Verified: StatusEnabled},
				{Phone: "18177778888", Name: "赵生", IDCard: "310230200001013603", BankName: "建设银行", BankCard: "6222020000000002", GroupName: "内部", Balance: 10002, Status: StatusEnabled, FundStatus: StatusEnabled, Verified: StatusEnabled},
				{Phone: "19000000000", Name: "李和雨", IDCard: "110112198008080139", BankName: "中国银行", BankCard: "6222020000000003", GroupName: "内部", Balance: 840451.77, TotalProfit: 10300000, TotalLoss: 13433821, Status: StatusEnabled, FundStatus: StatusEnabled, Verified: StatusEnabled},
			}
			if err := tx.Create(&customers).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return err
}

func seedDefaultNewsSources(db *gorm.DB) error {
	defaultSources := []NewsSource{
		{
			Name:            "第一财经金融频道",
			SourceType:      "api",
			BaseURL:         "https://www.yicai.com/api/ajax/getjuhelist?action=finance",
			CategoryMapping: `{"金融":"FINANCE","A股":"A_SHARE","港股":"HK_STOCK","美股":"US_STOCK"}`,
			Enabled:         true,
			IntervalSeconds: 600,
			TimeoutSeconds:  15,
			RateLimit:       20,
			ConfigJSON:      `{"adapter":"yicai_finance","category":"FINANCE","include_content":false,"max_items":30,"max_response_bytes":2097152}`,
		},
		{
			Name:            "第一财经热门",
			SourceType:      "api",
			BaseURL:         "https://www.yicai.com/api/ajax/getjuhelist?action=hot",
			CategoryMapping: `{"A股":"A_SHARE","海外市场频道":"US_STOCK","产经":"INDUSTRY","地产":"FINANCE"}`,
			Enabled:         true,
			IntervalSeconds: 600,
			TimeoutSeconds:  15,
			RateLimit:       20,
			ConfigJSON:      `{"adapter":"yicai_hot","category":"FINANCE","include_content":false,"max_items":30,"max_response_bytes":2097152}`,
		},
	}
	for _, source := range defaultSources {
		var existing NewsSource
		err := db.Where("name = ?", source.Name).First(&existing).Error
		if err == nil {
			continue
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := db.Create(&source).Error; err != nil {
			return err
		}
	}
	return nil
}

const legacyLegalStatement = `<h2>一、信息说明</h2><p>本应用提供的行情、图表及相关信息仅用于信息展示，不构成任何投资建议、收益承诺或交易邀约。</p><h2>二、使用规范</h2><p>用户应遵守适用法律法规，不得利用本应用从事违法交易、扰乱市场秩序或侵害他人合法权益的活动。</p><h2>三、风险承担</h2><p>证券投资存在价格波动和本金损失风险。用户应独立判断并承担投资决策产生的结果。</p><h2>四、数据与服务</h2><p>行情可能受数据源、网络或系统维护影响而延迟或中断，最终交易结果以系统实际记录为准。</p>`

const defaultLegalStatement = `<h2>一、总则与适用范围</h2>
<p>1.1 为维护平台使用秩序、保护用户合法权益，本声明对行情浏览、账户使用、信息发布及交易相关功能的使用作出说明。用户应在使用相关功能前阅读本声明及对应业务规则。</p>
<p>1.2 用户和平台均应遵守适用法律法规。本声明不替代用户协议、隐私政策或具体业务协议，也不排除、限制用户依法享有的权利；条款与法律强制性规定不一致的，以法律规定为准。</p>
<p>1.3 平台运营主体、联系方式及依法需要公示的经营资质，应以经核验的正式公示信息为准。本声明、公司注册材料或页面图片本身不构成证券业务许可、收益担保或监管机构背书；使用涉及资金的功能前，请核实服务主体和业务资格。</p>
<h2>二、用户行为规范</h2>
<h3>2.1 遵守法律与市场规则</h3>
<p>不得利用平台实施洗钱、诈骗、非法集资、内幕交易、操纵市场、非法证券咨询或其他违法活动；不得假冒证券经营机构、从业人员或监管机构，不得编造、传播可能误导投资者的证券信息。</p>
<h3>2.2 信息发布与交流</h3>
<p>如使用评论、交流或信息发布功能，应对所发布内容的真实性、合法性及授权情况负责。不得发布虚假宣传、保证收益、荐股引流、商业招揽、恶意诋毁、骚扰、人身攻击或侵犯他人合法权益的内容。</p>
<p>不得在公开区域披露他人的身份证件、账户、银行卡、住址、联系方式等个人信息；不得以发布群聊、二维码或站外链接等方式诱导用户向未经核实的主体转账。</p>
<h3>2.3 账户与系统安全</h3>
<p>应妥善保管登录凭证和交易密码，不得出租、出借、买卖账户或冒用他人身份。发现账户被盗用、异常交易或信息泄露时，应及时修改凭证并通过平台公布的客服渠道反馈。</p>
<p>不得非法侵入系统、绕过权限校验、窃取数据、传播恶意程序，或以异常请求等方式干扰正常服务。未经授权不得批量抓取、复制、出售平台数据。</p>
<h2>三、平台管理与违规处理</h2>
<p>3.1 对涉嫌违法或违反已公示规则的行为，平台可在必要范围内采取提示、核实、限制相关功能、移除违规内容或暂停服务等措施。除法律另有规定、依法应予保密或存在紧急安全风险外，应说明处理原因并提供申诉渠道。</p>
<p>3.2 用户可通过平台公布的客服渠道提交投诉、举报或申诉，并提供必要的事实材料。平台应依法核实处理，不得以本声明为由任意扣留用户合法资金或剥夺其法定救济权利。</p>
<p>3.3 对有权机关依法提出的调查、信息调取或账户处置要求，平台应核验其合法性，并仅在法定权限和必要范围内配合。相关资料处理仍应符合个人信息保护和保密要求。</p>
<h2>四、知识产权与第三方内容</h2>
<p>4.1 平台软件、页面设计及自行制作的文字、图表等内容，相关权利归其合法权利人所有。第三方行情、新闻、图片、商标及其他材料的权利归原权利人所有；展示或引用不代表权利转让或第三方认可。</p>
<p>4.2 未经合法授权，不得超出法律允许范围复制、改编、传播或商业使用相关内容。用户提交内容应确保拥有相应权利或授权，不应侵犯他人的著作权、商标权、名誉权或隐私权。</p>
<p>4.3 权利人认为平台内容侵害其合法权益的，可通过客服提交权属证明、具体内容位置及相关证据，平台将依法核实处理。第三方链接和材料不代表平台对其真实性、合法性或服务质量作出保证。</p>
<h2>五、行情、交易与投资风险</h2>
<p>5.1 行情、资讯、图表、排行、历史业绩及收益展示仅供参考，不构成针对任何个人的投资建议、交易邀约或保本保收益承诺。历史表现、示例测算及模拟结果不代表未来收益。</p>
<p>5.2 证券价格、市场流动性、汇率及政策变化均可能导致损失。涉及杠杆或融资时，价格波动可能放大损失，并可能触发追加保证金或强制平仓。用户应了解产品规则、费用及自身风险承受能力，独立作出判断。</p>
<p>5.3 行情和服务可能因数据源、网络、设备故障或维护而延迟、中断或出现差错。页面估值、浮动盈亏和预计费用不等于最终结算结果；成交、费用及资金变动应结合实际记录核对，发现异常应及时提出核查。</p>
<p>5.4 订单提交不等于成交，成交受交易时段、价格、数量、可用资金、可卖持仓及相应市场规则等因素影响。涉及港股等跨市场功能时，还应了解对应交易日、计价币种、交易单位和费用差异。</p>
<h2>六、个人信息与身份认证</h2>
<p>6.1 个人信息的收集、使用、保存及用户权利行使，应遵循适用法律和已公示的隐私政策，并限于明确、合理和必要的目的。身份证件、人脸照片、银行卡及账户资料不得用于未告知或未经合法授权的用途。</p>
<p>6.2 当前现场正脸采集用于提交材料供人工审核，不等同于专业活体检测、人证一致性核验或第三方权威认证，也不保证能够识别所有冒用行为。审核通过不代表对投资能力、账户安全或收益作出担保。</p>
<p>6.3 用户可依法申请查询、更正或删除个人信息，以及撤回相关授权；具体流程通过隐私政策或客服渠道了解。依法需要继续保存的资料，应在法定范围和期限内妥善处理。</p>
<h2>七、资金与账户操作提示</h2>
<p>7.1 办理资金划转前，应核对经确认的收款主体、银行卡、币种、金额及业务规则，不向未经核实的个人或第三方账户转账，不向他人提供交易密码或验证码。</p>
<p>7.2 银转证申请提交不等于资金已经到账，应核对实际汇款、审核状态和账户流水。证转银申请、审核通过与银行实际到账也可能存在时间差；收费、处理时限及限制条件应以事先公示并依法适用的规则为准。</p>
<p>7.3 认证与审核是相关账户功能的使用条件，不构成平台合法经营资格的证明。对资金状态、交易记录或费用存在疑问时，应保留凭证并及时联系平台核查，必要时通过有权机构寻求救济。</p>
<h2>八、责任边界、变更与争议处理</h2>
<p>8.1 用户应对自身违法行为及有过错的操作依法承担责任；平台也应对自身行为承担相应法定义务。本声明不免除平台依法不得免除的责任，不排除因平台故意、重大过失或其他依法应负责的情形产生的责任。</p>
<p>8.2 因不可抗力、第三方原因或技术故障影响服务时，应根据具体事实和适用法律确定责任，平台应采取合理措施减轻影响，不作“一切损失均由用户承担”的概括性免责。</p>
<p>8.3 声明更新应在本页或其他适当位置公示。涉及用户重大权益的变更，应依法作出显著提示，并在需要时另行征得同意；不得仅以用户浏览页面视为其放弃法定权利或作出所有授权。</p>
<p>8.4 对本声明或服务有疑问、投诉或争议，可通过平台公布的客服渠道联系处理。协商不成的，可依适用法律及合法有效的争议解决约定寻求救济；任何条款无效，不影响其他合法条款的效力。</p>`

func seedDefaultArticles(db *gorm.DB) error {
	articles := []AppArticle{
		{
			Title:   "新手必看",
			Type:    "新手必看",
			Status:  StatusEnabled,
			Content: `<h2>开始交易前</h2><p>请先了解证券市场风险，完成实名认证，并确认账户和资金状态正常。</p><h2>交易规则</h2><p>当前支持 A 股市价交易。普通股票买入数量须为 100 股的整数倍，科创板最低买入 200 股。</p><h2>交易时间</h2><p>默认交易时段为工作日 09:30–11:30、13:00–15:00，实际以订单提交时的系统校验为准。</p><h2>风险提示</h2><p>证券价格可能大幅波动，请根据自身风险承受能力审慎决策。</p>`,
		},
		{
			Title:   "法律声明",
			Type:    "法律声明",
			Status:  StatusEnabled,
			Content: defaultLegalStatement,
		},
		{
			Title:   "帮助中心",
			Type:    "帮助中心",
			Status:  StatusEnabled,
			Content: `<h2>行情与搜索</h2><p>可通过首页或行情页按证券代码、名称搜索，并查看指数、排行和证券详情。</p><h2>自选与持仓</h2><p>在证券详情页可添加自选或提交交易；持仓和成交记录可在交易页面查看。</p><h2>常见问题</h2><p>交易失败时，请检查交易时段、实名认证、账户状态、可用余额、可卖持仓和数量规则。</p><h2>服务范围</h2><p>当前港股、限价委托与撤单功能尚未开放。</p>`,
		},
	}

	for _, article := range articles {
		if article.Type == "法律声明" {
			// 只升级原始默认正文，保留后台自定义文章及启用状态。
			if err := db.Model(&AppArticle{}).
				Where("type = ? AND title = ? AND content = ?", article.Type, article.Title, legacyLegalStatement).
				Update("content", article.Content).Error; err != nil {
				return err
			}
		}
		var count int64
		if err := db.Model(&AppArticle{}).Where("type = ? AND deleted_at IS NULL", article.Type).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			if err := db.Create(&article).Error; err != nil {
				return err
			}
		}
	}
	return nil
}

func resetSequences(db *gorm.DB) error {
	tables := []string{
		"system_tenants", "system_menus", "system_roles", "system_departments", "system_users",
		"system_menu_auths", "system_tenant_menu_scopes", "system_tenant_auth_scopes", "customers", "customer_fund_records",
	}

	for _, table := range tables {
		seqName := table + "_id_seq"
		query := fmt.Sprintf("SELECT setval('%s', (SELECT COALESCE(MAX(id), 1) FROM %s));", seqName, table)
		if err := db.Exec(query).Error; err != nil {
			zap.L().Error("failed to reset sequence", zap.String("sequence", seqName), zap.Error(err))
			return err
		}
		zap.L().Info("sequence reset successfully", zap.String("sequence", seqName))
	}
	return nil
}

func migrateCustomers(db *gorm.DB) error {
	var count int64
	if err := db.Model(&Customer{}).Count(&count).Error; err != nil || count > 0 {
		return err
	}
	customers := []Customer{
		{Phone: "18559134815", Name: "王瑶", IDCard: "330324198802063556", BankName: "农业银行", BankCard: "6228597863664878", GroupName: "内部", Balance: 372770.77, FrozenBalance: 184121, TotalLoss: 230000, Status: StatusEnabled, FundStatus: StatusEnabled, Verified: StatusEnabled},
		{Phone: "18988888888", Name: "潘斌", IDCard: "330324198502063568", BankName: "工商银行", BankCard: "6222020000000001", GroupName: "内部", Balance: 499999.95, Status: StatusEnabled, FundStatus: StatusEnabled, Verified: StatusEnabled},
		{Phone: "18177778888", Name: "赵生", IDCard: "310230200001013603", BankName: "建设银行", BankCard: "6222020000000002", GroupName: "内部", Balance: 10002, Status: StatusEnabled, FundStatus: StatusEnabled, Verified: StatusEnabled},
		{Phone: "19000000000", Name: "李和雨", IDCard: "110112198008080139", BankName: "中国银行", BankCard: "6222020000000003", GroupName: "内部", Balance: 840451.77, TotalProfit: 10300000, TotalLoss: 13433821, Status: StatusEnabled, FundStatus: StatusEnabled, Verified: StatusEnabled},
	}
	if err := db.Create(&customers).Error; err != nil {
		return err
	}
	return seedCustomerSupportingData(db)
}

func seedCustomerSupportingData(db *gorm.DB) error {
	var deviceCount int64
	if err := db.Model(&CustomerDevice{}).Count(&deviceCount).Error; err != nil || deviceCount > 0 {
		return err
	}
	var customers []Customer
	if err := db.Find(&customers).Error; err != nil {
		return err
	}
	devices := make([]CustomerDevice, 0, len(customers))
	for index, customer := range customers {
		devices = append(devices, CustomerDevice{CustomerID: customer.ID, DeviceType: "ios", Brand: "Apple", DeviceModel: "iPhone 13", DeviceID: fmt.Sprintf("device-%d", customer.ID), APIBaseURL: "https://api.stock.local/client", System: "iOS 17", AppVersion: "3.8.2", Blocked: StatusDisabled, LastLogin: customer.UpdatedAt.Unix()})
		if index == 0 {
			_ = db.Create(&CustomerFundRecord{CustomerID: customer.ID, Type: "资金存入", Direction: "入账", Currency: "CNY", Amount: 10000, Balance: customer.Balance, Remark: "系统初始化"}).Error
		}
	}
	return db.Create(&devices).Error
}

func seedTradePositions(db *gorm.DB) error {
	var count int64
	if err := db.Model(&TradePosition{}).Count(&count).Error; err != nil || count > 0 {
		return err
	}
	var customers []Customer
	if err := db.Find(&customers).Error; err != nil {
		return err
	}
	names := []string{"平安银行", "贵州茅台", "万科A", "中国石油"}
	positions := make([]TradePosition, 0, len(customers))
	for index, customer := range customers {
		quantity := 1000 + float64(index)*500
		cost := 11.8 + float64(index)*8
		price := 12.5 + float64(index)*8
		positions = append(positions, TradePosition{CustomerID: customer.ID, Symbol: fmt.Sprintf("6000%02d.SH", index+1), StockName: names[index%len(names)], Currency: "CNY", PositionQty: quantity, AvailableQty: quantity - 100, CurrentPrice: price, CostPrice: cost, TotalCost: quantity * cost, Margin: quantity * cost / 5, Leverage: 5, MarketValue: quantity * price, ProfitLoss: quantity * (price - cost), ProfitRate: (price - cost) / cost * 100, Status: StatusEnabled, BuyAt: time.Now().AddDate(0, 0, -index-1).Unix()})
	}
	return db.Create(&positions).Error
}

func seedTradeRecords(db *gorm.DB) error {
	var count int64
	if err := db.Model(&TradeRecord{}).Count(&count).Error; err != nil || count >= 40 {
		return err
	}
	var positions []TradePosition
	if err := db.Find(&positions).Error; err != nil {
		return err
	}
	if len(positions) == 0 {
		return nil
	}
	names := []string{"欣天科技", "大恒发电", "开开实业", "华大九天", "宜安股份", "永辉股份", "国风新材", "长安科技"}
	records := make([]TradeRecord, 0, 40-int(count))
	for index := 0; index < 40-int(count); index++ {
		position := positions[index%len(positions)]
		direction := "买入"
		if index%3 == 2 {
			direction = "卖出"
		}
		quantity := float64((index%8 + 1) * 1000)
		price := position.CostPrice + float64(index%5-2)*0.23
		amount := price * quantity
		commission := amount * 0.0003
		stampDuty := float64(0)
		if direction == "卖出" {
			stampDuty = amount * 0.0005
		}
		transferFee := quantity * 0.00001
		records = append(records, TradeRecord{CustomerID: position.CustomerID, Symbol: fmt.Sprintf("%06d.%s", 300000+index*17, []string{"SZ", "SH"}[index%2]), StockName: names[index%len(names)], Currency: "CNY", Direction: direction, TradePrice: price, Quantity: quantity, Amount: amount, StampDuty: stampDuty, TransferFee: transferFee, Commission: commission, Remark: fmt.Sprintf("%s %.0f股", direction, quantity), TradeAt: time.Now().Add(-time.Duration(index) * 17 * time.Minute).Unix()})
	}
	return db.Create(&records).Error
}

func seedFinanceRecords(db *gorm.DB) error {
	var count int64
	if err := db.Model(&FinanceRecharge{}).Count(&count).Error; err != nil || count > 0 {
		return err
	}
	var customers []Customer
	if err := db.Find(&customers).Error; err != nil || len(customers) == 0 {
		return err
	}
	recharges := make([]FinanceRecharge, 0, len(customers))
	withdrawals := make([]FinanceWithdrawal, 0, len(customers))
	for index, customer := range customers {
		amount := float64((index + 1) * 100000)
		recharges = append(recharges, FinanceRecharge{RequestID: fmt.Sprintf("seed-recharge-%d", customer.ID), CustomerID: customer.ID, Amount: amount, Currency: "CNY", Method: "支付宝转账", Status: StatusEnabled, Remark: "后台确认到账", ReviewedAt: time.Now().Add(-time.Duration(index) * time.Hour).Unix()})
		withdrawals = append(withdrawals, FinanceWithdrawal{RequestID: fmt.Sprintf("seed-withdrawal-%d", customer.ID), CustomerID: customer.ID, Amount: amount / 2, Currency: "CNY", Method: "银行卡", BankName: customer.BankName, BankCard: customer.BankCard, BankAddress: customer.BankAddress, Status: StatusEnabled, ReviewedAt: time.Now().Add(-time.Duration(index) * 2 * time.Hour).Unix()})
	}
	if err := db.Create(&recharges).Error; err != nil {
		return err
	}
	return db.Create(&withdrawals).Error
}

func Migrate(db *gorm.DB) error {
	err := migrateTable(db)
	if err != nil {
		return err
	}
	err = migrateData(db)
	if err != nil {
		return err
	}
	err = migrateCustomers(db)
	if err != nil {
		return err
	}
	err = seedCustomerSupportingData(db)
	if err != nil {
		return err
	}
	err = seedTradePositions(db)
	if err != nil {
		return err
	}
	err = seedTradeRecords(db)
	if err != nil {
		return err
	}
	err = seedAppSystemSetting(db)
	if err != nil {
		return err
	}
	err = migrateMarginCallSettings(db)
	if err != nil {
		return err
	}
	err = migrateLeveragedPositions(db)
	if err != nil {
		return err
	}
	err = seedFinanceRecords(db)
	if err != nil {
		return err
	}
	// 添加序列重置操作
	err = resetSequences(db)
	if err != nil {
		return err
	}
	return nil
}

func migrateLeveragedPositions(db *gorm.DB) error {
	var setting AppSystemSetting
	if err := db.First(&setting).Error; err != nil {
		return err
	}
	var config struct {
		Risk struct {
			DefaultLeverage    float64 `json:"defaultLeverage"`
			AppLeverageEnabled bool    `json:"appLeverageEnabled"`
		} `json:"risk"`
	}
	if err := json.Unmarshal([]byte(setting.Config), &config); err != nil {
		return err
	}
	if !config.Risk.AppLeverageEnabled || config.Risk.DefaultLeverage < 1 {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var positions []TradePosition
		if err := tx.Where("position_qty > 0 AND total_cost > 0 AND COALESCE(margin, 0) <= 0").Find(&positions).Error; err != nil {
			return err
		}
		refunds := make(map[uint]float64)
		for index := range positions {
			position := &positions[index]
			position.Leverage = config.Risk.DefaultLeverage
			position.Margin = math.Round(position.TotalCost/position.Leverage*100) / 100
			refunds[position.CustomerID] += position.TotalCost - position.Margin
			if err := tx.Save(position).Error; err != nil {
				return err
			}
		}
		for customerID, refund := range refunds {
			if err := tx.Model(&Customer{}).Where("id = ?", customerID).Update("balance", gorm.Expr("balance + ?", refund)).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func migrateMarginCallSettings(db *gorm.DB) error {
	var setting AppSystemSetting
	if err := db.First(&setting).Error; err != nil {
		return err
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(setting.Config), &config); err != nil {
		return err
	}
	risk, ok := config["risk"].(map[string]any)
	if !ok {
		risk = make(map[string]any)
		config["risk"] = risk
	}
	risk["marginCallStart"] = float64(16)
	risk["marginCallRate"] = 0.01
	content, err := json.Marshal(config)
	if err != nil {
		return err
	}
	return db.Model(&setting).Update("config", string(content)).Error
}

func seedAppSystemSetting(db *gorm.DB) error {
	var count int64
	if err := db.Model(&AppSystemSetting{}).Count(&count).Error; err != nil || count > 0 {
		return err
	}
	config := `{"branding":{"productName":"证券行情","logo":""},"trade":{"buyCommission":0.0003,"sellCommission":0.0003,"minCommission":5,"stampDuty":0.0005,"transferFee":0.0001,"morningStart":"09:30:00","morningEnd":"11:30:00","afternoonStart":"13:00:00","afternoonEnd":"15:00:00","allDay":false,"nonTradingFee":false},"hkTrade":{"commission":0.00025,"minCommission":15,"stampDuty":0.001,"regulatoryFee":0.000027,"tradingFee":0.0000565,"settlementFee":0.000042},"stockSync":{"enabled":true,"tradingIntervalSecs":60,"offHoursIntervalSecs":600,"maxSyncRows":10000},"risk":{"defaultLeverage":5,"forceCloseRatio":0.8,"appLeverageEnabled":true,"managementFeePerTenThousand":2.8,"marginCallStart":16,"marginCallRate":0.01},"recharge":{"minRecharge":5000,"quickAmounts":"5000,10000,100000,300000,500000,1000000","minWithdraw":100,"withdrawFeeRate":0,"minWithdrawFee":0,"dailyWithdrawLimit":1,"withdrawStart":"09:30:00","withdrawEnd":"15:00:00","sameDaySellWithdraw":true},"limits":{"starBoard":0.16,"beijingBoard":0.24,"mainBoard":0.08,"growthBoard":0.16,"minStarShares":200,"stTrade":false,"newStockTrade":false},"links":{"customerService":"https://service.example.com","hkdRate":0.89,"aQuote":"https://quotes.example.com/#/mobile/","hkQuote":"https://quotes.example.com/#/mobile/","telegramToken":"","telegramChatId":""}}`
	return db.Create(&AppSystemSetting{Config: config}).Error
}

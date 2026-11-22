package system

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
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
		&AdminSound{},
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
		&PositionSettlementReview{},
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
<p>5.2 证券价格、市场流动性、汇率及政策变化均可能导致损失。涉及孖展时，价格波动可能放大损失，并可能触发追加保证金或强制平仓。用户应了解产品规则、费用及自身风险承受能力，独立作出判断。</p>
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
			Title:  "新手必看",
			Type:   "新手必看",
			Status: StatusEnabled,
			Content: `<h2>第一次使用，先看这里</h2><p>本页面介绍平台模拟交易功能和账户操作。展示的行情可能延迟，模拟成交不代表交易所真实成交，也不构成投资建议。</p>
<h2>下单前先核对</h2><ol><li>确认证券代码、名称、市场和买卖方向。</li><li>检查订单数量、预计金额及可用资金。</li><li>核对交易时间、证券状态和页面显示的价格。</li><li>查看佣金、税费、融资及管理费等可能产生的成本。</li><li>提交后在订单、成交记录和资金流水中核对结果。</li></ol>
<h2>了解本金和杠杆</h2><p>当前新开A股及港股模拟仓位按平台设置使用5倍融资。杠杆会放大仓位和盈亏，费用也会另行计入；强平条件及账户实际金额以账户和订单页面为准，亏损不保证仅限于初始本金。</p>
<h2>先熟悉账户页面</h2><p>可用余额、冻结金额、基础本金、融资金额和持仓市值是不同数据。提交申请不等于资金已经到账；请以资金流水、交易记录和银行实际到账为准。</p>
<h2>行情与成交</h2><p>行情可能延迟，屏幕显示价格不保证成为成交价格。请在提交后核对订单状态、成交记录、持仓数量及费用明细。</p>
<h2>遇到异常怎么办</h2><p>保存证券代码、订单号、发生时间和页面截图，再通过平台客服核查。不要仅凭行情估值判断实际成交、到账或可卖数量。</p>`,
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
		{
			Title:  "交易规则（A股）",
			Type:   "交易规则（A股）",
			Status: StatusEnabled,
			Content: `<h2>一、平台交易方式</h2><p>本平台提供模拟交易功能，实际开放的证券、委托类型和交易权限以页面为准。模拟委托及成交不等于向交易所发出真实委托。</p>
<h2>二、交易时间</h2><p>默认交易时段为工作日 09:30–11:30、13:00–15:00。节假日、临时休市和后台模拟交易设置可能影响可下单时间；最终以订单提交时的系统校验为准。</p>
<h2>三、买入数量与市场规则</h2><p>普通A股常见最小买入单位为100股；科创板、北交所、ETF、新股及特殊证券可能有不同数量或价格限制。下单面板显示的最小数量、步长和校验提示优先于本说明。</p>
<h2>四、价格、停牌与订单结果</h2><p>涨跌幅限制、停复牌、证券状态和报价可用性会影响委托。市价委托不保证按屏幕上的最新报价成交；如提交失败或未成交，请查看订单状态和具体提示。</p>
<h2>五、A股可卖时间与零股</h2><p>A股通常按T+1规则管理可卖数量，新买入的股份当日通常不可卖。卖出时还要考虑冻结数量和最小交易单位；部分卖出与全部卖出的零股处理可能不同，以实际持仓和服务端校验为准。</p>
<h2>六、可用资金与费用</h2><p>下单所需金额除成交金额外还可能包含佣金、税费和其他费用。账户显示的可用余额、冻结金额、本金和融资金额含义不同，请勿将持仓市值全部视为可提现资金。</p>
<h2>七、融资与强制平仓风险</h2><p>当前新开A股模拟仓位按平台设置使用5倍融资。融资会放大盈亏；担保比例不足或费用扣收可能触发追缴或强制平仓。强平价格、执行时点及剩余负债以账户实际记录为准，亏损不保证限于初始本金。</p>
<h2>八、成交后核对</h2><p>请核对成交记录、持仓数量、可卖数量、资金流水和费用明细。发现差异时保存订单号、证券代码和时间后联系客服处理。</p>`,
		},
		{
			Title:  "交易规则（港股）",
			Type:   "交易规则（港股）",
			Status: StatusEnabled,
			Content: `<h2>一、平台交易方式与时段</h2><p>港股模拟交易使用港币账户。通常交易时段为香港工作日 09:30–12:00、13:00–16:00；节假日、特殊交易时段及后台设置可能影响委托，具体以订单提交时的校验为准。</p>
<h2>二、每手股数与买卖数量</h2><p>港股每只证券的每手股数可能不同，不可一律按100股计算。下单前查看该证券详情和订单面板显示的每手股数、最小买入量及可卖数量。</p>
<h2>三、港币账户与汇率</h2><p>港股交易以港币核算，人民币和港币余额分别显示，不应直接相加。换汇、资金划转和费用以平台当前开放的功能及资金记录为准。</p>
<h2>四、买卖与交收</h2><p>平台港股模拟交易支持按系统规则进行当日买卖；可卖数量仍受持仓、委托和证券交易单位限制。模拟账本更新不代表真实市场交收完成。</p>
<h2>五、行情与委托</h2><p>公开报价可能延迟，价格变动、流动性和盘口数量会影响成交。报价时间、委托类型和成交结果以页面展示及服务端订单记录为准；展示报价不等于保证成交价。</p>
<h2>六、融资与风险</h2><p>当前新开港股模拟仓位按平台设置使用5倍融资，费用以港币计入。杠杆、汇率和流动性会放大账户波动；担保不足可能触发追缴或强平，亏损不保证仅限于初始本金。</p>
<h2>七、证券状态及交易限制</h2><p>证券未启用、停牌、交易单位不符、报价过期、余额不足或账户权限不满足时，系统可能拒绝委托。请以具体页面提示和订单记录为准。</p>
<h2>八、费用与记录</h2><p>港股相关费用以港币计算，费率、最低佣金和收取方向请查看“费用收取—港股”及下单预估。成交后以交易记录和港币资金流水核对。</p>`,
		},
		{
			Title:  "费用收取（A股）",
			Type:   "费用收取（A股）",
			Status: StatusEnabled,
			Content: `<h2>一、费用由哪些部分组成</h2><p>A股模拟交易可能涉及券商佣金、印花税、过户费和单独计算的持仓管理费用。不同费用的计费基数、收取方向和扣款时点可能不同。</p>
<h2>二、佣金</h2><p>平台当前默认佣金费率为买卖各0.03%，最低5元；按成交金额和后台设置计算，低于最低收费时按最低金额收取。后台可调整费率和最低收费。</p>
<h2>三、印花税</h2><p>平台当前默认按卖出成交金额的0.05%计算。印花税仅在系统设定的方向收取；费率及适用范围可能调整，请以订单页和成交明细为准。</p>
<h2>四、过户费</h2><p>平台当前默认费率为成交金额的0.01%；上海市场按买卖方向收取，其他市场按当前系统规则处理。实际收取方向以成交明细为准。</p>
<h2>五、持仓管理费用</h2><p>持仓管理费用与单笔买卖佣金不同，可能按持仓金额和后台规则单独计算。查看账户资金流水及相关费用说明，不要把它与成交成本混为一项。</p>
<h2>六、计算示例与核对</h2><p>按当前默认设置，1万元成交金额对应的佣金计算值为3元，但因最低佣金规则，单边佣金按5元计；卖出时默认印花税计算值为5元。该示例不含其他费用，后台设置变化后结果也会变化。</p>
<p>订单页展示的是预计费用；成交后请以交易记录和资金流水为准。费率或最低收费以后台当前配置为准。</p>`,
		},
		{
			Title:  "费用收取（港股）",
			Type:   "费用收取（港股）",
			Status: StatusEnabled,
			Content: `<h2>一、费用组成</h2><p>平台港股模拟交易的费用项目包括佣金、印花税、证监会征费、交易费和系统交收费。费用以港币核算，并按后台规则分别计算。</p>
<h2>二、当前默认费率</h2><ul><li>佣金：成交金额的0.025%，最低15港币。</li><li>印花税：默认0.1%。</li><li>证监会征费：默认0.0027%。</li><li>交易费：默认0.00565%。</li><li>系统交收费：默认0.0042%。</li></ul>
<p>上述为平台当前默认模拟参数，不代表所有市场或券商的统一收费标准。后台可调整费率、最低收费和适用方向，以下单页面为准。</p>
<h2>三、计算示例</h2><p>假设单笔成交金额为1万港币，按当前默认费率计算：佣金比例结果为2.5港币，但低于最低佣金，因此按15港币计；其余费用按各自费率计算并依平台规则舍入。买卖方向不同，实际项目可能不同。</p>
<h2>四、如何核对</h2><p>下单前查看港币账户的费用预估；成交后对照成交回执、交易记录和港币资金流水。若金额不一致，请提供订单号联系客服核查。</p>`,
		},
		{
			Title:  "常见问题",
			Type:   "常见问题",
			Status: StatusEnabled,
			Content: `<h2>一、为什么订单提交失败？</h2><p>请检查交易时间、实名认证、账户状态、证券状态、可用资金、买卖数量和页面提示。最终以服务端提交时的校验结果为准。</p>
<h2>二、A股买入后为什么当天不能卖？</h2><p>A股按平台设置执行T+1可卖规则，新买入的数量通常需要到下一交易日才可卖出；港股模拟交易的可卖规则不同，具体看持仓页面。</p>
<h2>三、什么是5倍融资？亏损会封顶吗？</h2><p>融资会让持仓名义金额高于自有本金，同时放大盈亏和费用影响。保证金不足可能触发追缴或强制平仓；极端行情、价格跳空或处理延迟时，亏损不保证只限于本金。</p>
<h2>四、为什么可卖数量与持仓数量不同？</h2><p>新买入数量、冻结数量、交易市场和零股规则都可能影响可卖数量。请以持仓页“可卖”字段及下单校验为准；无法处理的零股请联系客服核查。</p>
<h2>五、费用在哪里查看？</h2><p>提交前查看订单页预计费用；成交后查看交易记录、成交回执和资金流水。预计费用与实际扣款以系统计算结果及后台当前费率为准。</p>
<h2>六、行情价格一定是实时成交价吗？</h2><p>不一定。公开行情可能延迟，报价只供展示；模拟成交价格和可成交状态以提交订单时的服务端校验及成交记录为准。</p>
<h2>七、分红、送股或除权后持仓变化怎么办？</h2><p>公司行为可能改变股数、参考价格或资产显示。请保留证券代码、发生时间和截图联系客服核对，不要仅凭估值判断资金是否到账。</p>
<h2>八、入金或出金多久完成？</h2><p>提交申请不等于银行转账已完成。处理时间受银行、审核、申请时点和节假日影响；请以资金记录状态和实际银行到账为准。</p>`,
		},
	}

	legacyGuideContent := map[string]string{
		"新手必看": `<h2>开始交易前</h2><p>请先了解证券市场风险，完成实名认证，并确认账户和资金状态正常。</p><h2>交易规则</h2><p>当前支持 A 股市价交易。普通股票买入数量须为 100 股的整数倍，科创板最低买入 200 股。</p><h2>交易时间</h2><p>默认交易时段为工作日 09:30–11:30、13:00–15:00，实际以订单提交时的系统校验为准。</p><h2>风险提示</h2><p>证券价格可能大幅波动，请根据自身风险承受能力审慎决策。</p>`,
		"交易规则（A股）": strings.Join([]string{
			`<h2>一、交易方式与时间</h2><p>A股通过平台模拟交易功能提交买入或卖出。默认交易时间为工作日 09:30–11:30、13:00–15:00；实际以后台设置和提交时校验为准。</p>`,
			`<h2>二、买入数量</h2><p>普通A股买入数量为100股的整数倍。科创板最低买入数量以后台设置为准，当前默认200股；达到最低数量后可按整数股输入。</p>`,
			`<h2>三、卖出与交收</h2><p>A股实行T+1交收，新买入的股数通常需到下一交易日才可卖出。全部卖出时可按系统规则处理持仓中的零股；可卖数量以持仓账本为准。</p>`,
			`<h2>四、交易限制</h2><p>ST股票、新股、涨幅限制、实名认证、账户状态、资金及持仓等条件可能影响下单。系统会按实际配置校验并显示拒绝原因。</p>`,
			`<h2>五、孖展与风险</h2><p>如账户启用孖展，所需本金、费用、追缴担保金及强制平仓条件以账户页面和后台当前规则为准。价格波动可能造成损失，不应将模拟估值视为收益保证。</p>`,
		}, "\n"),
		"交易规则（港股）": strings.Join([]string{
			`<h2>一、交易方式与时间</h2><p>港股功能须由后台启用，使用独立港币账户进行模拟交易。常规交易时段按香港交易日及后台配置执行，通常为工作日 09:30–12:00、13:00–16:00；节假日、特殊时段及实际可交易状态以系统校验为准。</p>`,
			`<h2>二、每手与数量</h2><p>港股每手股数因证券而异，买入数量须符合该证券当前交易单位。下单页面显示的每手股数和可买数量以接口返回为准。</p>`,
			`<h2>三、交收与账户</h2><p>港股模拟交易使用港币账户；港股支持T+0模拟买卖，实际可卖数量仍以持仓账本为准。人民币与港币余额分别核算。</p>`,
			`<h2>四、报价与风险</h2><p>页面行情为公开报价，可能延迟，不代表交易所实时行情或真实成交。报价过期、证券未启用、资金或交易权限不满足时，系统会拒绝下单。</p>`,
			`<h2>五、孖展与风险</h2><p>如账户启用孖展，所需本金、费用、追缴担保金及强制平仓条件以账户页面和后台当前规则为准。证券价格、汇率及市场流动性变化均可能造成损失。</p>`,
		}, "\n"),
		"费用收取（A股）": strings.Join([]string{
			`<h2>一、费用项目</h2><p>A股交易费用可能包括佣金、过户费、印花税及单独计算的持仓管理费用。适用项目、费率和最低收费以后台当前设置为准。</p>`,
			`<h2>二、默认收取规则</h2><p>系统默认佣金费率为买卖各0.03%，最低5元；印花税默认仅卖出收取，费率0.05%；过户费默认费率0.01%，上海市场买卖收取，其他市场卖出时收取。后台配置可以调整这些默认值。</p>`,
			`<h2>三、如何核对</h2><p>订单面板中的预计费用仅供参考。成交后请在交易记录和资金流水核对实际扣费；持仓管理费用按后台规则另行计算。</p>`,
		}, "\n"),
		"费用收取（港股）": strings.Join([]string{
			`<h2>一、费用项目</h2><p>港股模拟交易费用由佣金、印花税、证监会征费、交易费及系统交收费等项目组成。</p>`,
			`<h2>二、默认费率</h2><p>系统默认佣金为成交金额的0.025%，最低15港币；印花税为0.1%；证监会征费为0.0027%；交易费为0.00565%；系统交收费为0.0042%。具体费率及买卖收取方向以后台当前设置和订单页面为准。</p>`,
			`<h2>三、如何核对</h2><p>下单页面展示的费用为预计值，实际金额以成交回执、交易记录和港币流水为准。相关费率可能调整，提交前请核对订单页面显示。</p>`,
		}, "\n"),
		"常见问题": strings.Join([]string{
			`<h2>一、为什么订单提交失败？</h2><p>请核对交易时间、实名认证、账户状态、资金余额、可买或可卖数量、证券状态及页面提示。最终以服务端提交时的校验结果为准。</p>`,
			`<h2>二、孖展和强制平仓是什么？</h2><p>孖展会放大仓位，也会放大价格波动对账户的影响。担保金不足或发生费用扣收时，可能触发追加担保金或强制平仓；具体阈值以后台当前规则为准，不能据此认定损失必然仅限于本金。</p>`,
			`<h2>三、持仓零股怎么卖？</h2><p>若部分卖出受整手规则限制，可按页面允许的数量分笔卖出；若仍有零股无法处理，请联系客服核对。全部卖出的零股处理以当前交易校验为准。</p>`,
			`<h2>四、分红、送股或除权后持仓有变化怎么办？</h2><p>公司行为可能影响持股数量、参考价格和资产显示。如页面数据与公告或账户记录不一致，请保留证券代码和截图并联系客服核查，不要仅凭估值自行判断到账情况。</p>`,
			`<h2>五、入金和出金多久到账？</h2><p>平台页面提交申请不等于银行转账已完成。处理时间会受到银行、申请时间、审核和节假日影响，请在资金记录查看状态；具体以页面提示和实际银行到账为准。</p>`,
		}, "\n"),
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
		if legacyContent, ok := legacyGuideContent[article.Type]; ok {
			// 只升级未被后台修改过的旧默认稿，保留自定义文章。
			if err := db.Model(&AppArticle{}).
				Where("type = ? AND title = ? AND content = ?", article.Type, article.Title, legacyContent).
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
	config := `{"branding":{"productName":"证券行情","logo":""},"trade":{"buyCommission":0.0003,"sellCommission":0.0003,"minCommission":5,"stampDuty":0.0005,"transferFee":0.0001,"morningStart":"09:30:00","morningEnd":"11:30:00","afternoonStart":"13:00:00","afternoonEnd":"15:00:00","allDay":false,"nonTradingFee":false},"hkTrade":{"commission":0.00025,"minCommission":15,"stampDuty":0.001,"regulatoryFee":0.000027,"tradingFee":0.0000565,"settlementFee":0.000042},"stockSync":{"enabled":true,"tradingIntervalSecs":60,"offHoursIntervalSecs":600,"maxSyncRows":10000},"risk":{"defaultLeverage":5,"forceCloseRatio":0.8,"appLeverageEnabled":true,"managementFeePerTenThousand":2.8,"marginCallStart":16,"marginCallRate":0.01},"recharge":{"minRecharge":5000,"quickAmounts":"5000,10000,100000,300000,500000,1000000","minWithdraw":100,"withdrawFeeRate":0,"minWithdrawFee":0,"dailyWithdrawLimit":1,"withdrawStart":"09:30:00","withdrawEnd":"15:00:00","sameDaySellWithdraw":true},"limits":{"starBoard":0.16,"beijingBoard":0.24,"mainBoard":0.08,"growthBoard":0.16,"minStarShares":200,"stTrade":false,"newStockTrade":false},"links":{"customerService":"","hkdRate":0.89,"aQuote":"https://quotes.example.com/#/mobile/","hkQuote":"https://quotes.example.com/#/mobile/","telegramToken":"","telegramChatId":""}}`
	return db.Create(&AppSystemSetting{Config: config}).Error
}

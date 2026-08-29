package system

import (
	"errors"
	"fmt"
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
		&FinanceRecharge{},
		&FinanceWithdrawal{},
		&CustomerDevice{},
		&TradePosition{},
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
			Name:            "国务院政策发布",
			SourceType:      "api",
			BaseURL:         "https://www.gov.cn/pushinfo/v150203/pushinfo.json",
			CategoryMapping: `{"政策":"POLICY","财经":"FINANCE","经济":"ECONOMY"}`,
			Enabled:         true,
			IntervalSeconds: 600,
			TimeoutSeconds:  10,
			RateLimit:       20,
			ConfigJSON:      `{"adapter":"gov_cn_pushinfo","category":"POLICY","include_content":false,"max_items":40,"request_timeout_ms":10000,"max_response_bytes":2097152}`,
		},
		{
			Name:            "新浪财经",
			SourceType:      "html",
			BaseURL:         "https://finance.sina.com.cn/",
			Enabled:         true,
			IntervalSeconds: 600,
			TimeoutSeconds:  15,
			RateLimit:       20,
			ConfigJSON:      `{"adapter":"sina_finance","category":"FINANCE","include_content":false,"max_items":30,"max_response_bytes":4194304}`,
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
			Content: `<h2>一、信息说明</h2><p>本应用提供的行情、图表及相关信息仅用于信息展示，不构成任何投资建议、收益承诺或交易邀约。</p><h2>二、使用规范</h2><p>用户应遵守适用法律法规，不得利用本应用从事违法交易、扰乱市场秩序或侵害他人合法权益的活动。</p><h2>三、风险承担</h2><p>证券投资存在价格波动和本金损失风险。用户应独立判断并承担投资决策产生的结果。</p><h2>四、数据与服务</h2><p>行情可能受数据源、网络或系统维护影响而延迟或中断，最终交易结果以系统实际记录为准。</p>`,
		},
		{
			Title:   "帮助中心",
			Type:    "帮助中心",
			Status:  StatusEnabled,
			Content: `<h2>行情与搜索</h2><p>可通过首页或行情页按证券代码、名称搜索，并查看指数、排行和证券详情。</p><h2>自选与持仓</h2><p>在证券详情页可添加自选或提交交易；持仓和成交记录可在交易页面查看。</p><h2>常见问题</h2><p>交易失败时，请检查交易时段、实名认证、账户状态、可用余额、可卖持仓和数量规则。</p><h2>服务范围</h2><p>当前港股、限价委托与撤单功能尚未开放。</p>`,
		},
	}

	for _, article := range articles {
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
		positions = append(positions, TradePosition{CustomerID: customer.ID, Symbol: fmt.Sprintf("6000%02d.SH", index+1), StockName: names[index%len(names)], Currency: "CNY", PositionQty: quantity, AvailableQty: quantity - 100, CurrentPrice: price, CostPrice: cost, TotalCost: quantity * cost, MarketValue: quantity * price, ProfitLoss: quantity * (price - cost), ProfitRate: (price - cost) / cost * 100, Status: StatusEnabled, BuyAt: time.Now().AddDate(0, 0, -index-1).Unix()})
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

func seedAppSystemSetting(db *gorm.DB) error {
	var count int64
	if err := db.Model(&AppSystemSetting{}).Count(&count).Error; err != nil || count > 0 {
		return err
	}
	config := `{"branding":{"productName":"证券行情","logo":""},"trade":{"buyCommission":0,"sellCommission":0,"minCommission":0,"stampDuty":0.0005,"transferFee":0.0001,"managementFee":0.00028,"morningStart":"09:30:00","morningEnd":"11:30:00","afternoonStart":"13:00:00","afternoonEnd":"15:00:00","allDay":false,"nonTradingFee":false},"stockSync":{"enabled":true,"tradingIntervalSecs":60,"offHoursIntervalSecs":600,"maxSyncRows":10000},"risk":{"defaultLeverage":5,"forceCloseRatio":0.8,"appLeverageEnabled":false},"recharge":{"minRecharge":5000,"quickAmounts":"5000,10000,100000,300000,500000,1000000","minWithdraw":100,"withdrawFeeRate":0,"minWithdrawFee":0,"dailyWithdrawLimit":1,"withdrawStart":"09:30:00","withdrawEnd":"15:00:00","sameDaySellWithdraw":true},"limits":{"starBoard":0.16,"beijingBoard":0.24,"mainBoard":0.08,"growthBoard":0.16,"minStarShares":200,"stTrade":false,"newStockTrade":false},"links":{"customerService":"https://service.example.com","hkdRate":0.89,"aQuote":"https://quotes.example.com/#/mobile/","hkQuote":"https://quotes.example.com/#/mobile/","telegramToken":"","telegramChatId":""}}`
	return db.Create(&AppSystemSetting{Config: config}).Error
}

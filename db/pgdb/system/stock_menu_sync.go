package system

import (
	"fmt"

	"gorm.io/gorm"
)

type stockMenuDefinition struct {
	Name      string
	Path      string
	Component string
	Title     string
	Icon      string
	Parent    string
	Sort      uint
}

type stockMenuAuthDefinition struct {
	MenuName string
	Title    string
	Mark     string
}

func syncStockAdminMenus(tx *gorm.DB) error {
	if err := tx.Exec("DELETE FROM system_roles__system_auths").Error; err != nil {
		return err
	}
	if err := tx.Exec("DELETE FROM system_roles__system_menus").Error; err != nil {
		return err
	}
	if err := tx.Unscoped().Where("1 = 1").Delete(&SystemTenantAuthScope{}).Error; err != nil {
		return err
	}
	if err := tx.Unscoped().Where("1 = 1").Delete(&SystemTenantMenuScope{}).Error; err != nil {
		return err
	}
	if err := tx.Unscoped().Where("1 = 1").Delete(&SystemMenuAuth{}).Error; err != nil {
		return err
	}
	if err := tx.Unscoped().Where("1 = 1").Delete(&SystemMenu{}).Error; err != nil {
		return err
	}

	definitions := []stockMenuDefinition{
		{"Dashboard", "/dashboard", "/index/index", "首页", "ri-home-line", "", 1},
		{"DashboardConsole", "console", "/dashboard/console/index", "工作台", "", "Dashboard", 1},
		{"User", "/user", "/index/index", "用户管理", "ri-user-line", "", 2},
		{"UserList", "list", "/user/list/index", "用户列表", "", "User", 1},
		{"Device", "devices", "/user/devices/index", "接口列表", "ri-links-line", "User", 2},
		{"UserFund", "funds", "/user/funds/index", "资金流水", "", "User", 3},
		{"UserBank", "banks", "/user/banks/index", "银行卡", "", "User", 4},
		{"UserAuth", "verification", "/user/verification/index", "实名认证", "", "User", 5},
		{"Trade", "/trade", "/index/index", "交易管理", "ri-line-chart-line", "", 3},
		{"TradePosition", "position", "/trade/position/index", "持仓列表", "", "Trade", 1},
		{"TradeRecord", "record", "/trade/record/index", "交易记录", "", "Trade", 2},
		{"Business", "/business", "/index/index", "财务管理", "ri-money-cny-circle-line", "", 4},
		{"BusinessRecharge", "recharge", "/business/recharge/index", "充值列表", "", "Business", 1},
		{"BusinessWithdrawal", "withdrawal", "/business/withdrawal/index", "提现申请", "", "Business", 2},
		{"Stock", "/stock", "/index/index", "证券列表", "ri-stock-line", "", 5},
		{"StockInfo", "info", "/stock/info/index", "证券信息", "", "Stock", 1},
		{"Settings", "/settings", "/index/index", "应用配置", "ri-settings-3-line", "", 6},
		{"SettingsSystem", "system", "/settings/system/index", "系统设置", "", "Settings", 1},
		{"SettingsNotice", "notice", "/settings/notice/index", "公告管理", "", "Settings", 2},
		{"SettingsArticle", "article", "/settings/article/index", "文章管理", "", "Settings", 3},
		{"System", "/system", "/index/index", "系统管理", "ri-settings-line", "", 7},
		{"SystemUser", "admin", "/system/admin/index", "管理员列表", "", "System", 1},
		{"SystemRole", "role", "/system/role/index", "角色管理", "", "System", 2},
		{"SystemMenu", "menu", "/system/menu/index", "菜单管理", "", "System", 3},
	}

	menuIDs := make(map[string]uint, len(definitions))
	for _, definition := range definitions {
		parentID := menuIDs[definition.Parent]
		level := uint(1)
		if definition.Parent != "" {
			level = 2
		}
		menu := SystemMenu{
			Path: definition.Path, Name: definition.Name, Component: definition.Component, Title: definition.Title,
			Icon: definition.Icon, ShowBadge: 2, IsHide: 2, IsHideTab: 2, IsIframe: 2, KeepAlive: 2,
			Status: StatusEnabled, Level: level, ParentID: parentID, Sort: definition.Sort,
		}
		if err := tx.Create(&menu).Error; err != nil {
			return fmt.Errorf("create menu %s: %w", definition.Name, err)
		}
		menuIDs[definition.Name] = menu.ID
	}

	authDefinitions := []stockMenuAuthDefinition{
		{"Device", "封禁", "user:device:update"},
		{"TradePosition", "删除", "trade:position:delete"}, {"TradePosition", "修改", "trade:position:update"}, {"TradePosition", "新增", "trade:position:create"},
		{"TradeRecord", "删除", "trade:record:delete"}, {"TradeRecord", "修改", "trade:record:update"}, {"TradeRecord", "新增", "trade:record:create"},
		{"StockInfo", "编辑", "stock:info:update"}, {"StockInfo", "删除", "stock:info:delete"}, {"StockInfo", "新增", "stock:info:create"},
		{"SettingsSystem", "编辑", "settings:system:update"},
		{"SettingsNotice", "增加", "settings:notice:create"}, {"SettingsNotice", "修改", "settings:notice:update"}, {"SettingsNotice", "删除", "settings:notice:delete"},
		{"SettingsArticle", "增加", "settings:article:create"}, {"SettingsArticle", "修改", "settings:article:update"}, {"SettingsArticle", "删除", "settings:article:delete"},
		{"SystemUser", "编辑", "system:user:update"}, {"SystemUser", "删除", "system:user:delete"}, {"SystemUser", "新增", "system:user:create"},
		{"SystemRole", "新增", "system:role:create"}, {"SystemRole", "编辑", "system:role:update"}, {"SystemRole", "删除", "system:role:delete"},
		{"SystemMenu", "新增", "system:menu:create"}, {"SystemMenu", "编辑", "system:menu:update"}, {"SystemMenu", "删除", "system:menu:delete"},
	}
	for _, definition := range authDefinitions {
		if err := tx.Create(&SystemMenuAuth{MenuID: menuIDs[definition.MenuName], Title: definition.Title, Mark: definition.Mark}).Error; err != nil {
			return fmt.Errorf("create menu permission %s: %w", definition.Mark, err)
		}
	}
	return nil
}

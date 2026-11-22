package system

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestDefaultLegalStatement(t *testing.T) {
	for _, heading := range []string{
		"一、总则与适用范围", "二、用户行为规范", "三、平台管理与违规处理",
		"四、知识产权与第三方内容", "五、行情、交易与投资风险", "六、个人信息与身份认证",
		"七、资金与账户操作提示", "八、责任边界、变更与争议处理",
	} {
		if !strings.Contains(defaultLegalStatement, "<h2>"+heading+"</h2>") {
			t.Errorf("法律声明缺少章节：%s", heading)
		}
	}
	for _, required := range []string{
		"不构成证券业务许可", "不等同于专业活体检测", "本声明不免除平台依法不得免除的责任",
		"银转证申请提交不等于资金已经到账",
	} {
		if !strings.Contains(defaultLegalStatement, required) {
			t.Errorf("法律声明缺少必要说明：%s", required)
		}
	}
}

func TestSeedDefaultArticlesIntegration(t *testing.T) {
	dsn := os.Getenv("ARTICLE_TEST_DSN")
	if dsn == "" {
		t.Skip("未配置文章隔离测试数据库")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	schema := fmt.Sprintf("article_test_%d", time.Now().UnixNano())
	if err := db.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Error(err)
		}
	}()
	if err := db.Exec("SET search_path TO " + schema).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&AppArticle{}); err != nil {
		t.Fatal(err)
	}

	t.Run("新库初始化", func(t *testing.T) {
		if err := seedDefaultArticles(db); err != nil {
			t.Fatal(err)
		}
		var articles []AppArticle
		if err := db.Find(&articles).Error; err != nil {
			t.Fatal(err)
		}
		if len(articles) != 8 {
			t.Fatalf("默认文章数量不符：%d", len(articles))
		}
		for _, articleType := range []string{
			"新手必看", "法律声明", "帮助中心", "交易规则（A股）", "交易规则（港股）",
			"费用收取（A股）", "费用收取（港股）", "常见问题",
		} {
			var article AppArticle
			if err := db.Where("type = ?", articleType).First(&article).Error; err != nil ||
				article.Status != StatusEnabled || strings.TrimSpace(article.Content) == "" {
				t.Errorf("默认文章类型 %q 应启用且包含正文：%v", articleType, err)
			}
		}
		var legal AppArticle
		if err := db.Where("type = ?", "法律声明").First(&legal).Error; err != nil {
			t.Fatal(err)
		}
		if legal.Content != defaultLegalStatement || legal.Status != StatusEnabled {
			t.Fatal("新库应发布完整法律声明")
		}
	})

	t.Run("只升级原始默认且重复执行不变", func(t *testing.T) {
		if err := db.Exec("TRUNCATE TABLE app_articles RESTART IDENTITY").Error; err != nil {
			t.Fatal(err)
		}
		fixtures := []AppArticle{
			{Title: "法律声明", Type: "法律声明", Content: legacyLegalStatement, Status: StatusEnabled},
			{Title: "法律声明", Type: "法律声明", Content: legacyLegalStatement, Status: StatusDisabled},
			{Title: "法律声明", Type: "法律声明", Content: "<p>后台自定义条款</p>", Status: StatusEnabled},
			{Title: "自行修改的标题", Type: "法律声明", Content: legacyLegalStatement, Status: StatusEnabled},
			{Title: "帮助中心", Type: "帮助中心", Content: legacyLegalStatement, Status: StatusEnabled},
			{Title: "法律声明", Type: "法律声明", Content: legacyLegalStatement, Status: StatusEnabled},
		}
		for i := range fixtures {
			if err := db.Create(&fixtures[i]).Error; err != nil {
				t.Fatal(err)
			}
		}
		if err := db.Delete(&fixtures[5]).Error; err != nil {
			t.Fatal(err)
		}
		if err := seedDefaultArticles(db); err != nil {
			t.Fatal(err)
		}
		for i, original := range fixtures {
			var actual AppArticle
			if err := db.Unscoped().First(&actual, original.ID).Error; err != nil {
				t.Fatal(err)
			}
			expected := original.Content
			if i < 2 {
				expected = defaultLegalStatement
			}
			if actual.Content != expected || actual.Title != original.Title ||
				actual.Status != original.Status || actual.DeletedAt.Valid != original.DeletedAt.Valid {
				t.Errorf("文章%d被错误修改：%+v", i, actual)
			}
		}
		var before []AppArticle
		if err := db.Unscoped().Order("id").Find(&before).Error; err != nil {
			t.Fatal(err)
		}
		if err := seedDefaultArticles(db); err != nil {
			t.Fatal(err)
		}
		var after []AppArticle
		if err := db.Unscoped().Order("id").Find(&after).Error; err != nil {
			t.Fatal(err)
		}
		if len(after) != len(before) {
			t.Fatal("重复迁移不应新增文章")
		}
		for i := range before {
			if before[i].ID != after[i].ID || before[i].Content != after[i].Content ||
				!before[i].UpdatedAt.Equal(after[i].UpdatedAt) {
				t.Errorf("重复迁移不应更新文章%d", before[i].ID)
			}
		}
	})

	t.Run("扩充默认新手指南且保留后台自定义", func(t *testing.T) {
		if err := db.Exec("TRUNCATE TABLE app_articles RESTART IDENTITY").Error; err != nil {
			t.Fatal(err)
		}
		legacyRules := strings.Join([]string{
			`<h2>一、交易方式与时间</h2><p>A股通过平台模拟交易功能提交买入或卖出。默认交易时间为工作日 09:30–11:30、13:00–15:00；实际以后台设置和提交时校验为准。</p>`,
			`<h2>二、买入数量</h2><p>普通A股买入数量为100股的整数倍。科创板最低买入数量以后台设置为准，当前默认200股；达到最低数量后可按整数股输入。</p>`,
			`<h2>三、卖出与交收</h2><p>A股实行T+1交收，新买入的股数通常需到下一交易日才可卖出。全部卖出时可按系统规则处理持仓中的零股；可卖数量以持仓账本为准。</p>`,
			`<h2>四、交易限制</h2><p>ST股票、新股、涨幅限制、实名认证、账户状态、资金及持仓等条件可能影响下单。系统会按实际配置校验并显示拒绝原因。</p>`,
			`<h2>五、孖展与风险</h2><p>如账户启用孖展，所需本金、费用、追缴担保金及强制平仓条件以账户页面和后台当前规则为准。价格波动可能造成损失，不应将模拟估值视为收益保证。</p>`,
		}, "\n")
		articles := []AppArticle{
			{Title: "交易规则（A股）", Type: "交易规则（A股）", Content: legacyRules, Status: StatusDisabled},
			{Title: "常见问题", Type: "常见问题", Content: "<p>管理员维护的常见问题</p>", Status: StatusDisabled},
		}
		for i := range articles {
			if err := db.Create(&articles[i]).Error; err != nil {
				t.Fatal(err)
			}
		}
		if err := seedDefaultArticles(db); err != nil {
			t.Fatal(err)
		}

		var updated AppArticle
		if err := db.Where("type = ? AND title = ?", "交易规则（A股）", "交易规则（A股）").First(&updated).Error; err != nil {
			t.Fatal(err)
		}
		if updated.Content == legacyRules || !strings.Contains(updated.Content, "八、成交后核对") ||
			strings.Count(updated.Content, "<h2>") < 8 || updated.Status != StatusDisabled {
			t.Fatal("应扩充旧默认内容并保留文章状态")
		}

		var custom AppArticle
		if err := db.Where("type = ? AND title = ?", "常见问题", "常见问题").First(&custom).Error; err != nil {
			t.Fatal(err)
		}
		if custom.Content != "<p>管理员维护的常见问题</p>" || custom.Status != StatusDisabled {
			t.Fatal("不应覆盖后台自定义内容或状态")
		}
	})
}

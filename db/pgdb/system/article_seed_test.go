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
		if len(articles) != 3 {
			t.Fatalf("默认文章数量不符：%d", len(articles))
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
}

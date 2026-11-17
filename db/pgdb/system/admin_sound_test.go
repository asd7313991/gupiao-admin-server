package system

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestAdminSoundEventsIntegration(t *testing.T) {
	dsn := os.Getenv("SOUND_TEST_DSN")
	if dsn == "" {
		t.Skip("未配置声音提醒隔离测试数据库 SOUND_TEST_DSN")
	}
	root, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	rootSQL, err := root.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer rootSQL.Close()
	schema := fmt.Sprintf("sound_test_%d", time.Now().UnixNano())
	if err := root.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := root.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Error(err)
		}
	}()
	db, err := gorm.Open(postgres.Open(dsn+" search_path="+schema), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	if err := db.AutoMigrate(&AdminSound{}, &FinanceRecharge{}, &Customer{}); err != nil {
		t.Fatal(err)
	}
	sound := AdminSound{Kind: "recharge", Enabled: true, Filename: "sound.wav", AudioHash: "original", Data: []byte{1, 2, 3}}
	if err := db.Create(&sound).Error; err != nil {
		t.Fatal(err)
	}
	const count = 8
	var wg sync.WaitGroup
	failures := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			record := FinanceRecharge{RequestID: fmt.Sprintf("sound-%d", i), CustomerID: 1, Amount: 10, Status: 2, Model: gorm.Model{CreatedAt: time.Unix(1, 0)}}
			failures <- CreateWithAdminSound(db, &record, "recharge")
		}(i)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	check := func() {
		t.Helper()
		var current AdminSound
		if err := db.First(&current, "kind = ?", "recharge").Error; err != nil {
			t.Fatal(err)
		}
		if current.Revision != count || !current.Enabled || current.AudioHash != "original" || len(current.Data) != 3 {
			t.Fatalf("计数丢失或覆盖音频配置：%+v", current)
		}
		var total int64
		if err := db.Model(&FinanceRecharge{}).Count(&total).Error; err != nil || total != count {
			t.Fatalf("业务记录数量不符：%d，%v", total, err)
		}
	}
	check()
	duplicate := FinanceRecharge{RequestID: "sound-0", CustomerID: 1}
	if err := CreateWithAdminSound(db, &duplicate, "recharge"); err == nil {
		t.Fatal("重复业务应失败")
	}
	rollback := errors.New("模拟业务回滚")
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := RecordAdminSoundEvent(tx, "recharge"); err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	check()
	for _, kind := range []string{"withdrawal", "verification", "registration"} {
		if err := db.Transaction(func(tx *gorm.DB) error { return RecordAdminSoundEvent(tx, kind) }); err != nil {
			t.Fatal(err)
		}
		var current AdminSound
		if err := db.First(&current, "kind = ?", kind).Error; err != nil || current.Revision != 1 {
			t.Fatalf("首次事件遗漏：%s %v", kind, err)
		}
	}
	if err := db.AutoMigrate(&AdminSound{}); err != nil {
		t.Fatal(err)
	}
	check()
}

package mobile

import "testing"

func TestNoticeVisibleToCustomer(t *testing.T) {
	for _, tc := range []struct {
		name       string
		recipients string
		customerID uint
		visible    bool
	}{
		{name: "all customers", recipients: "[]", customerID: 8, visible: true},
		{name: "matching customer", recipients: "[3,8,12]", customerID: 8, visible: true},
		{name: "other customer", recipients: "[3,12]", customerID: 8, visible: false},
		{name: "legacy empty value", recipients: "", customerID: 8, visible: true},
		{name: "invalid legacy value", recipients: "not-json", customerID: 8, visible: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if visible := noticeVisibleToCustomer(tc.recipients, tc.customerID); visible != tc.visible {
				t.Fatalf("noticeVisibleToCustomer(%q, %d) = %t, want %t", tc.recipients, tc.customerID, visible, tc.visible)
			}
		})
	}
}

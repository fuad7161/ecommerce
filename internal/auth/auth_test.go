package auth

import "testing"

func TestPermissions(t *testing.T) {
	admin := &Admin{Role: RoleAdmin, Active: true}
	staff := &Admin{Role: RoleStaff, Active: true}
	disabled := &Admin{Role: RoleAdmin, Active: false}
	var nobody *Admin

	for _, p := range []Permission{PermViewDashboard, PermManageOrders, PermViewCustomers, PermManageCourier} {
		if !staff.Can(p) || !admin.Can(p) {
			t.Errorf("admin and staff should have %s", p)
		}
	}
	for _, p := range []Permission{PermManageCatalog, PermManageStock, PermManageSetting, PermViewAudit, PermManageOffers} {
		if staff.Can(p) {
			t.Errorf("staff must not have %s", p)
		}
		if !admin.Can(p) {
			t.Errorf("admin should have %s", p)
		}
	}
	if disabled.Can(PermViewDashboard) || nobody.Can(PermViewDashboard) {
		t.Error("inactive or missing admins have no permissions")
	}
}

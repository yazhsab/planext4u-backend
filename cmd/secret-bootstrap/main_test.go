package main

import (
	"os"
	"reflect"
	"testing"
)

func TestFileSecretsOnlyAcceptsExplicitNonEmptyFileValues(t *testing.T) {
	values := fileSecrets([]string{"BOOKING_OTP_KEY_FILE_VALUE=synthetic-key", "DATABASE_URL=postgres://ignored", "lower_FILE_VALUE=ignored", "EMPTY_FILE_VALUE="})
	if !reflect.DeepEqual(values, map[string]string{"BOOKING_OTP_KEY_FILE_VALUE": "synthetic-key"}) {
		t.Fatalf("secrets=%#v", values)
	}
}

func TestSupplementaryGroupsRequireExplicitBoundedNonRootConfiguration(t *testing.T) {
	for _, environment := range [][]string{nil, {"OTHER=101"}} {
		groups, err := supplementaryGroups(environment)
		if err != nil || len(groups) != 0 {
			t.Fatalf("inherited group authorization: %v %v", groups, err)
		}
	}
	groups, err := supplementaryGroups([]string{"PLANEXT4U_SUPPLEMENTARY_GROUPS=101,102"})
	if err != nil || !reflect.DeepEqual(groups, []int{101, 102}) {
		t.Fatalf("groups=%v %v", groups, err)
	}
	for _, value := range []string{"", "0", "-1", "65536", "65532", "101,101", " 101", "0101", "+101", "root", "1,2,3,4,5,6,7,8,9"} {
		if _, err := supplementaryGroups([]string{"PLANEXT4U_SUPPLEMENTARY_GROUPS=" + value}); err == nil {
			t.Fatalf("unsafe group config accepted: %q", value)
		}
	}
	if _, err := supplementaryGroups([]string{"PLANEXT4U_SUPPLEMENTARY_GROUPS=101", "PLANEXT4U_SUPPLEMENTARY_GROUPS=102"}); err == nil {
		t.Fatal("ambiguous groups accepted")
	}
}

func TestRunMaterializesSecretWithRestrictedModeAndExecutesService(t *testing.T) {
	var target, contents string
	var mode os.FileMode
	set := map[string]string{}
	unset := ""
	executed := ""
	arguments := []string{}
	chowned := []string{}
	dropped := []string{}
	err := run([]string{"secret-bootstrap", "/migrate", "-service", "all"}, []string{"BOOKING_OTP_KEY_FILE_VALUE=synthetic-key"}, func(key, value string) error {
		set[key] = value
		return nil
	}, func(key string) error {
		unset = key
		return nil
	}, func(path string, permission os.FileMode) error {
		if path != secretDirectory || permission != 0o700 {
			t.Fatalf("directory=%s mode=%o", path, permission)
		}
		return nil
	}, func(path string, data []byte, permission os.FileMode) error {
		target, contents, mode = path, string(data), permission
		return nil
	}, func(path string, uid, gid int) error {
		if uid != 65532 || gid != 65532 {
			t.Fatalf("ownership=%d:%d", uid, gid)
		}
		chowned = append(chowned, path)
		return nil
	}, func(groups []int) error {
		if len(groups) != 0 {
			t.Fatalf("supplementary groups=%v", groups)
		}
		dropped = append(dropped, "groups")
		return nil
	}, func(gid int) error {
		if gid != 65532 {
			t.Fatalf("gid=%d", gid)
		}
		dropped = append(dropped, "gid")
		return nil
	}, func(uid int) error {
		if uid != 65532 {
			t.Fatalf("uid=%d", uid)
		}
		dropped = append(dropped, "uid")
		return nil
	}, func(path string, args []string, _ []string) error {
		executed = path
		arguments = append([]string(nil), args...)
		return nil
	})
	if err != nil || executed != "/migrate" || !reflect.DeepEqual(arguments, []string{"/migrate", "-service", "all"}) || !reflect.DeepEqual(chowned, []string{secretDirectory, target}) || !reflect.DeepEqual(dropped, []string{"groups", "gid", "uid"}) || target == "" || contents != "synthetic-key" || mode != 0o600 || unset != "BOOKING_OTP_KEY_FILE_VALUE" || set["BOOKING_OTP_KEY_FILE"] != target {
		t.Fatalf("err=%v executed=%s args=%#v chowned=%#v dropped=%#v target=%s contents=%s mode=%o unset=%s set=%#v", err, executed, arguments, chowned, dropped, target, contents, mode, unset, set)
	}
}

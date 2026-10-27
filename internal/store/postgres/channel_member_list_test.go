package postgres

import (
	"reflect"
	"slices"
	"testing"

	"telesrv/internal/domain"
)

// 受限名单的 SQL 限制项必须与 domain.ChannelBannedRights 的字段完全一致：以后加了限制项而这里
// 没加，只带该项限制的成员就不会出现在受限名单里。
func TestChannelRestrictionRightKeysMatchDomain(t *testing.T) {
	rightsType := reflect.TypeOf(domain.ChannelBannedRights{})
	var want []string
	for i := 0; i < rightsType.NumField(); i++ {
		field := rightsType.Field(i)
		if field.Name == "ViewMessages" || field.Name == "UntilDate" {
			continue
		}
		if tag := field.Tag.Get("json"); tag != "" {
			t.Fatalf("ChannelBannedRights.%s has json tag %q; banned_rights keys are assumed to be field names", field.Name, tag)
		}
		want = append(want, field.Name)
	}
	got := slices.Clone(channelRestrictionRightKeys)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Fatalf("channelRestrictionRightKeys = %v, want the restriction fields of domain.ChannelBannedRights %v", got, want)
	}
}

package connector

import (
	"context"
	"crypto/sha256"
	"fmt"
	"maunium.net/go/mautrix/bridgev2"
	"maunium.net/go/mautrix/bridgev2/networkid"
	"net/url"
	"teamsbridge.local/teamsbridge/internal/graph"
	"time"
)

type profileCacheEntry struct {
	avatar  *bridgev2.Avatar
	name    string
	expires time.Time
}

func (c *Client) profile(ctx context.Context, user, name string) *bridgev2.UserInfo {
	info := &bridgev2.UserInfo{}
	if name != "" {
		info.Name = &name
	}
	if (c.meta != nil && c.meta.Auth != nil && c.meta.Auth.Profile != "photos") || (c.main != nil && !c.main.Config.ProfilePhotos && (c.meta == nil || c.meta.Auth == nil)) {
		return info
	}
	c.profileMu.Lock()
	cached, ok := c.profiles[user]
	c.profileMu.Unlock()
	if ok && time.Now().Before(cached.expires) {
		info.Avatar = cached.avatar
		if name == "" && cached.name != "" {
			info.Name = &cached.name
		}
		return info
	}
	path := "/users/" + url.PathEscape(user)
	if name == "" {
		var u graph.User
		if c.api.Do(ctx, "GET", path+"?$select=displayName", nil, &u) == nil && u.DisplayName != "" {
			name = u.DisplayName
			info.Name = &name
		}
	}
	var photo []byte
	err := c.api.Do(ctx, "GET", path+"/photo/$value", nil, &photo)
	ttl := 6 * time.Hour
	if err == nil {
		hash := sha256.Sum256(photo)
		info.Avatar = &bridgev2.Avatar{ID: networkid.AvatarID(fmt.Sprintf("%x", hash)), Get: func(context.Context) ([]byte, error) { return photo, nil }}
	} else {
		ttl = 10 * time.Minute
		if ge, ok := err.(*graph.APIError); ok && ge.RetryAfter > ttl {
			ttl = ge.RetryAfter
		}
	}
	c.profileMu.Lock()
	defer c.profileMu.Unlock()
	if c.profiles == nil {
		c.profiles = map[string]profileCacheEntry{}
	}
	if len(c.profiles) >= 256 {
		for key := range c.profiles {
			delete(c.profiles, key)
			break
		}
	}
	c.profiles[user] = profileCacheEntry{info.Avatar, name, time.Now().Add(ttl)}
	return info
}
func (c *Client) enrichMembers(ctx context.Context, info *bridgev2.ChatInfo) {
	if info.Members == nil {
		return
	}
	for id, member := range info.Members.MemberMap {
		if ctx.Err() != nil {
			return
		}
		name := ""
		if member.UserInfo != nil && member.UserInfo.Name != nil {
			name = *member.UserInfo.Name
		}
		member.UserInfo = c.profile(ctx, string(id), name)
		info.Members.MemberMap[id] = member
	}
}

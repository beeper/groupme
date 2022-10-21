package main

import "fmt"

func (user *User) GetRemoteID() string {
	if user == nil || !user.GMID.Valid() {
		return ""
	}
	return user.GMID.String()
}

func (user *User) GetRemoteName() string {
	if user == nil || !user.GMID.Valid() {
		return ""
	}
	return fmt.Sprintf("+%s", user.GMID.String())
}

package main

import (
	"github.com/gofrs/uuid"
	"time"
)

type Identifier struct {
	Id *uuid.UUID `json:"id,omitempty"`
}

type Timestamps struct {
	CreatedAt *time.Time `json:"createdAt,omitempty"`
	UpdatedAt *time.Time `json:"updatedAt,omitempty"`
}

type Entity struct {
	Identifier

	EntityType *string `json:"entityType,omitempty"`
	Public     *bool   `json:"public,omitempty"`
	Views      *int32  `json:"views,omitempty"`

	Timestamps

	Owner       *User        `json:"owner,omitempty"`
	Accessibles []Accessible `json:"accessibles,omitempty"`
	Files       []File       `json:"files,omitempty"`
}

type EntityTrait struct {
	Identifier
	EntityId *uuid.UUID `json:"entityId,omitempty"`
}

type Accessible struct {
	EntityTrait

	UserId    uuid.UUID `json:"userId"`
	IsOwner   bool      `json:"isOwner"`
	CanView   bool      `json:"canView"`
	CanEdit   bool      `json:"canEdit"`
	CanDelete bool      `json:"canDelete"`

	Timestamps
}

type File struct {
	EntityTrait

	Type         string     `json:"type"`
	Url          string     `json:"url"`
	Mime         *string    `json:"mime,omitempty"`
	Size         *int64     `json:"size,omitempty"`
	Version      int        `json:"version,omitempty"`        // version of the file if versioned
	Deployment   string     `json:"deploymentType,omitempty"` // server or client if applicable
	Platform     string     `json:"platform,omitempty"`       // platform if applicable
	UploadedBy   *uuid.UUID `json:"uploadedBy,omitempty"`     // user that uploaded the file
	Width        *int       `json:"width,omitempty"`
	Height       *int       `json:"height,omitempty"`
	CreatedAt    time.Time  `json:"createdAt,omitempty"`
	UpdatedAt    *time.Time `json:"updatedAt,omitempty"`
	Index        int        `json:"variation,omitempty"`    // variant of the file if applicable (e.g. PDF pages)
	OriginalPath *string    `json:"originalPath,omitempty"` // original relative path to maintain directory structure (e.g. for releases)

	Timestamps
}

type User struct {
	Entity

	Email       *string    `json:"email,omitempty"`
	Name        *string    `json:"name"`
	IsActive    bool       `json:"isActive,omitempty"`
	IsAdmin     bool       `json:"isAdmin,omitempty"`
	IsMuted     bool       `json:"isMuted,omitempty"`
	IsBanned    bool       `json:"isBanned,omitempty"`
	IsInternal  bool       `json:"isInternal,omitempty"`
	ActivatedAt *time.Time `json:"activatedAt,omitempty"`
	AllowEmails bool       `json:"allowEmails,omitempty"`
}

// Release struct
type Release struct {
	Entity

	AppId          *uuid.UUID `json:"appId,omitempty"`
	AppName        string     `json:"appName,omitempty"`
	AppTitle       string     `json:"appTitle,omitempty"`
	AppDescription *string    `json:"appDescription"`
	AppUrl         *string    `json:"appUrl"`
	AppExternal    *bool      `json:"appExternal"`
	Version        string     `json:"version,omitempty"`
	CodeVersion    string     `json:"codeVersion,omitempty"`
	ContentVersion string     `json:"contentVersion,omitempty"`
	Name           *string    `json:"name,omitempty"`
	Description    *string    `json:"description,omitempty"`
	Archive        *bool      `json:"archive"`
}

type App struct {
	Entity

	Name              string `json:"name,omitempty"`
	Description       string `json:"description,omitempty"`
	Url               string `json:"url,omitempty"`
	PixelStreamingUrl string `json:"pixelStreamingUrl,omitempty"`
	PrivacyPolicyURL  string `json:"privacyPolicyURL,omitempty"`
	External          bool   `json:"external,omitempty"`
	Title             string `json:"title,omitempty"` // Display name
}

type Region struct {
	Entity
	Name string `json:"name,omitempty"`
}

type PixelStreamingInstance struct {
	Entity

	InstanceId *string    `json:"instanceId,omitempty"`
	ReleaseId  *uuid.UUID `json:"releaseId,omitempty"`
	RegionId   *uuid.UUID `json:"regionId,omitempty"`
	Host       *string    `json:"host,omitempty"`
	Port       *uint16    `json:"port,omitempty"`
	Status     *string    `json:"status,omitempty"`
}

type PixelStreamingInstanceMetadata struct {
	Id           *uuid.UUID `json:"id"`
	RegionId     *uuid.UUID `json:"regionId"`
	Host         *string    `json:"host,omitempty"`
	Port         *uint16    `json:"port,omitempty"`
	Status       *string    `json:"status,omitempty"`
	InstanceId   *string    `json:"instanceId,omitempty"`
	InstanceType *string    `json:"instanceType"`
}

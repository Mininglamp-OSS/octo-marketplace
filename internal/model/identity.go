package model

type Identity struct {
	UID              string              `json:"uid"`
	Name             string              `json:"name"`
	Role             string              `json:"role,omitempty"`
	Spaces           []string            `json:"spaces,omitempty"`
	OwnedBotsBySpace map[string][]string `json:"owned_bots_by_space,omitempty"`
	ContextIncluded  bool                `json:"context_included,omitempty"`
}

type BotIdentity struct {
	BotUID    string `json:"bot_uid"`
	BotName   string `json:"bot_name"`
	OwnerUID  string `json:"owner_uid"`
	OwnerName string `json:"owner_name"`
	SpaceID   string `json:"space_id"`
}

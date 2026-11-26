// Package interact implements interactive Discord primitives: modal form
// builders, stateful multi-step workflows with timeouts and conditional
// transitions, and persistent paginated dashboards supporting both classic
// embed layouts and Discord Components v2 layouts.
package interact

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"strings"
	"sync"
	"time"

	disgodiscord "github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/disgo/rest"
	"github.com/disgoorg/snowflake/v2"
	"github.com/google/uuid"

	voxdiscord "github.com/cafecito-games/vox/internal/discord"
	"github.com/cafecito-games/vox/internal/output"
)

// RouteCustomID classifies a component or modal custom_id by prefix.
func RouteCustomID(customID string) string {
	switch {
	case strings.HasPrefix(customID, "modal:"):
		return "modal"
	case strings.HasPrefix(customID, "wf:"):
		return "workflow"
	case strings.HasPrefix(customID, "dash:"):
		return "dashboard"
	case strings.HasPrefix(customID, "voice:"):
		return "voice"
	default:
		return ""
	}
}

// ModalFieldSpec defines one text input field inside a Discord modal.
type ModalFieldSpec struct {
	CustomID    string `json:"custom_id,omitempty"`
	Label       string `json:"label"`
	Style       string `json:"style,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	Default     string `json:"default,omitempty"`
	Value       string `json:"value,omitempty"`
	Required    *bool  `json:"required,omitempty"`
	MaxLength   int    `json:"max_length,omitempty"`
}

// ParseModalFieldFlag parses a CLI --field value formatted as "Label[:style[:required]]".
func ParseModalFieldFlag(raw string) (ModalFieldSpec, error) {
	parts := strings.Split(raw, ":")
	label := strings.TrimSpace(parts[0])
	if label == "" {
		return ModalFieldSpec{}, output.UsageError(fmt.Sprintf("invalid --field %q: label cannot be empty", raw))
	}
	style := "short"
	if len(parts) >= 2 && strings.TrimSpace(parts[1]) != "" {
		style = strings.ToLower(strings.TrimSpace(parts[1]))
	}
	required := len(parts) < 3 || !strings.EqualFold(strings.TrimSpace(parts[2]), "optional")
	return ModalFieldSpec{
		CustomID: Slugify(label),
		Label:    label,
		Style:    style,
		Required: &required,
	}, nil
}

// Slugify converts a display title into a safe lowercase identifier segment.
func Slugify(text string) string {
	var builder strings.Builder
	for _, r := range strings.ToLower(text) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' {
			builder.WriteRune(r)
		} else {
			builder.WriteByte('_')
		}
	}
	return builder.String()
}

// BuildModal constructs a disgo ModalCreate with the "modal:" custom_id prefix.
func BuildModal(title, customID string, fields []ModalFieldSpec) (disgodiscord.ModalCreate, error) {
	if strings.TrimSpace(title) == "" {
		title = "Form"
	}
	if strings.TrimSpace(customID) == "" {
		customID = uuid.NewString()[:8]
	}
	if !strings.HasPrefix(customID, "modal:") {
		customID = "modal:" + customID
	}
	if len(fields) == 0 {
		return disgodiscord.ModalCreate{}, output.UsageError("modal requires at least one field")
	}

	modal := disgodiscord.NewModalCreate(customID, title)
	for i, field := range fields {
		if strings.TrimSpace(field.Label) == "" {
			return disgodiscord.ModalCreate{}, output.UsageError(fmt.Sprintf("modal field %d missing label", i))
		}
		fieldID := field.CustomID
		if fieldID == "" {
			fieldID = fmt.Sprintf("field_%d", i)
		}
		inputStyle := disgodiscord.TextInputStyleShort
		if strings.EqualFold(field.Style, "paragraph") || strings.EqualFold(field.Style, "long") {
			inputStyle = disgodiscord.TextInputStyleParagraph
		}
		required := true
		if field.Required != nil {
			required = *field.Required
		}
		value := field.Default
		if value == "" {
			value = field.Value
		}

		input := disgodiscord.NewTextInput(fieldID, inputStyle).
			WithRequired(required)
		if field.Placeholder != "" {
			input = input.WithPlaceholder(field.Placeholder)
		}
		if value != "" {
			input = input.WithValue(value)
		}
		if field.MaxLength > 0 {
			input = input.WithMaxLength(field.MaxLength)
		}
		modal = modal.AddLabel(field.Label, input)
	}

	return modal, nil
}

// WorkflowStep describes one state in a multi-step interactive workflow.
type WorkflowStep struct {
	StepID     string                     `json:"step_id"`
	StepType   string                     `json:"step_type"` // message | select | modal | confirm
	Content    string                     `json:"content,omitempty"`
	Components []voxdiscord.ComponentSpec `json:"components,omitempty"`
	Fields     []ModalFieldSpec           `json:"fields,omitempty"`
	Next       any                        `json:"next,omitempty"` // string | map[string]any | nil
	Timeout    int                        `json:"timeout,omitempty"`
}

// WorkflowDefinition defines a full multi-step workflow.
type WorkflowDefinition struct {
	WorkflowID string         `json:"workflow_id"`
	Steps      []WorkflowStep `json:"steps"`
}

// Step returns the step matching stepID and its index, or (-1, false).
func (definition WorkflowDefinition) Step(stepID string) (WorkflowStep, int, bool) {
	for i, step := range definition.Steps {
		if step.StepID == stepID {
			return step, i, true
		}
	}
	return WorkflowStep{}, -1, false
}

// WorkflowState tracks a live workflow execution for one user.
type WorkflowState struct {
	WorkflowID    string         `json:"workflow_id"`
	UserID        string         `json:"user_id"`
	ChannelID     snowflake.ID   `json:"channel_id"`
	CurrentStep   string         `json:"current_step"`
	CollectedData map[string]any `json:"collected_data"`
	MessageID     snowflake.ID   `json:"message_id,omitempty"`
}

// V2GalleryItemSpec describes a single media item inside a Components v2 gallery block.
type V2GalleryItemSpec struct {
	URL         string `json:"url"`
	Description string `json:"description,omitempty"`
}

// V2BlockSpec describes a Components v2 layout block inside a dashboard page.
//
// Supported types: "container", "text", "section", "separator", "gallery", "buttons".
type V2BlockSpec struct {
	Type      string                    `json:"type"`
	Content   any                       `json:"content,omitempty"` // string or []string
	Visible   *bool                     `json:"visible,omitempty"`
	Spacing   string                    `json:"spacing,omitempty"` // small | large
	Items     json.RawMessage           `json:"items,omitempty"`
	Button    *voxdiscord.ComponentSpec `json:"button,omitempty"`
	Thumbnail string                    `json:"thumbnail,omitempty"`
	Children  []V2BlockSpec             `json:"children,omitempty"`
	Spoiler   bool                      `json:"spoiler,omitempty"`
	Accent    string                    `json:"accent,omitempty"`
}

// DashboardPage represents one page of a persistent dashboard.
type DashboardPage struct {
	Embed      map[string]any             `json:"embed,omitempty"`
	Components []voxdiscord.ComponentSpec `json:"components,omitempty"`
	Layout     string                     `json:"layout,omitempty"` // "embed" (default) or "v2"
	Blocks     []V2BlockSpec              `json:"blocks,omitempty"`
}

// IsV2 reports whether this page uses Discord Components v2 layout.
func (page DashboardPage) IsV2() bool {
	return strings.EqualFold(page.Layout, "v2")
}

// DashboardDefinition describes a persistent multi-page dashboard.
type DashboardDefinition struct {
	DashboardID     string                     `json:"dashboard_id"`
	Pages           []DashboardPage            `json:"pages"`
	RefreshInterval int                        `json:"refresh_interval,omitempty"`
	Components      []voxdiscord.ComponentSpec `json:"components,omitempty"`
}

// Validate checks that every page of a dashboard uses the exact same layout.
// Discord stamps IS_COMPONENTS_V2 permanently at message creation so mixing
// embed and v2 pages in one dashboard is impossible.
func (definition DashboardDefinition) Validate() error {
	if len(definition.Pages) == 0 {
		return output.UsageError("dashboard must define at least one page")
	}
	firstV2 := definition.Pages[0].IsV2()
	for i, page := range definition.Pages {
		if page.IsV2() != firstV2 {
			return output.UsageError(fmt.Sprintf(
				"dashboard %q mixes embed and v2 layouts on page %d; every page of one dashboard must use the same layout",
				definition.DashboardID,
				i,
			))
		}
		if page.IsV2() && len(page.Blocks) == 0 {
			return output.UsageError(fmt.Sprintf("v2 dashboard page %d must declare at least one block", i))
		}
	}
	return nil
}

// IsV2 reports whether the dashboard uses Components v2 layout.
func (definition DashboardDefinition) IsV2() bool {
	return len(definition.Pages) > 0 && definition.Pages[0].IsV2()
}

// DashboardState tracks a live rendered dashboard message.
type DashboardState struct {
	DashboardID string         `json:"dashboard_id"`
	ChannelID   snowflake.ID   `json:"channel_id"`
	MessageID   snowflake.ID   `json:"message_id,omitempty"`
	CurrentPage int            `json:"current_page"`
	Data        map[string]any `json:"data"`
}

// BuildV2Components converts a slice of V2BlockSpecs into disgo Components v2
// LayoutComponents, optionally appending pagination nav buttons.
func BuildV2Components(
	blocks []V2BlockSpec,
	dashboardID string,
	navButtons []disgodiscord.InteractiveComponent,
) ([]disgodiscord.LayoutComponent, error) {
	if len(blocks) == 0 {
		return nil, output.UsageError("v2 layout requires at least one block")
	}
	components := make([]disgodiscord.LayoutComponent, 0, len(blocks)+1)
	for _, block := range blocks {
		built, err := buildV2LayoutBlock(block, dashboardID)
		if err != nil {
			return nil, err
		}
		components = append(components, built)
	}
	if len(navButtons) > 0 {
		components = append(components, disgodiscord.NewActionRow(navButtons...))
	}
	return components, nil
}

func buildV2LayoutBlock(block V2BlockSpec, dashboardID string) (disgodiscord.LayoutComponent, error) {
	prefix := fmt.Sprintf("dash:%s:", dashboardID)
	switch strings.ToLower(strings.TrimSpace(block.Type)) {
	case "text":
		text, ok := block.Content.(string)
		if !ok || text == "" {
			return nil, output.UsageError("v2 text block requires non-empty content string")
		}
		return disgodiscord.NewTextDisplay(text), nil

	case "separator":
		spacing := disgodiscord.SeparatorSpacingSizeSmall
		if strings.EqualFold(block.Spacing, "large") {
			spacing = disgodiscord.SeparatorSpacingSizeLarge
		}
		sep := disgodiscord.NewSeparator(spacing)
		if block.Visible != nil {
			sep = sep.WithDivider(*block.Visible)
		}
		return sep, nil

	case "gallery":
		items, err := parseV2GalleryItems(block.Items)
		if err != nil {
			return nil, err
		}
		return disgodiscord.NewMediaGallery(items...), nil

	case "section":
		return buildV2Section(block, prefix)

	case "buttons":
		var specs []voxdiscord.ComponentSpec
		if err := json.Unmarshal(block.Items, &specs); err != nil || len(specs) == 0 {
			return nil, output.UsageError("v2 buttons block requires a non-empty items array")
		}
		buttons := make([]disgodiscord.InteractiveComponent, 0, len(specs))
		for _, spec := range specs {
			spec.Type = "button"
			btn, err := voxdiscord.BuildComponent(spec, prefix)
			if err != nil {
				return nil, err
			}
			buttons = append(buttons, btn)
		}
		return disgodiscord.NewActionRow(buttons...), nil

	case "container":
		if len(block.Children) == 0 {
			return nil, output.UsageError("v2 container block requires at least one child")
		}
		children := make([]disgodiscord.ContainerSubComponent, 0, len(block.Children))
		for _, child := range block.Children {
			built, err := buildV2LayoutBlock(child, dashboardID)
			if err != nil {
				return nil, err
			}
			sub, ok := built.(disgodiscord.ContainerSubComponent)
			if !ok {
				return nil, output.UsageError(fmt.Sprintf("block type %q cannot be nested inside a v2 container", child.Type))
			}
			children = append(children, sub)
		}
		container := disgodiscord.NewContainer(children...).WithSpoiler(block.Spoiler)
		if strings.TrimSpace(block.Accent) != "" {
			color, err := voxdiscord.ParseHexColor(block.Accent)
			if err != nil {
				return nil, err
			}
			container = container.WithAccentColor(color)
		}
		return container, nil

	default:
		return nil, output.UsageError(fmt.Sprintf("unsupported v2 block type %q", block.Type))
	}
}

func buildV2Section(block V2BlockSpec, prefix string) (disgodiscord.SectionComponent, error) {
	var lines []string
	switch content := block.Content.(type) {
	case string:
		if content != "" {
			lines = []string{content}
		}
	case []any:
		for _, item := range content {
			if s, ok := item.(string); ok && s != "" {
				lines = append(lines, s)
			}
		}
	case []string:
		lines = content
	}
	if len(lines) == 0 {
		return disgodiscord.SectionComponent{}, output.UsageError("v2 section block requires content")
	}

	subs := make([]disgodiscord.SectionSubComponent, 0, len(lines))
	for _, line := range lines {
		subs = append(subs, disgodiscord.NewTextDisplay(line))
	}

	var accessory disgodiscord.SectionAccessoryComponent
	if block.Button != nil {
		spec := *block.Button
		spec.Type = "button"
		btn, err := voxdiscord.BuildComponent(spec, prefix)
		if err != nil {
			return disgodiscord.SectionComponent{}, err
		}
		acc, ok := btn.(disgodiscord.SectionAccessoryComponent)
		if !ok {
			return disgodiscord.SectionComponent{}, output.UsageError("invalid section button accessory")
		}
		accessory = acc
	} else if strings.TrimSpace(block.Thumbnail) != "" {
		accessory = disgodiscord.NewThumbnail(block.Thumbnail)
	} else {
		return disgodiscord.SectionComponent{}, output.UsageError("v2 section block requires either a button or thumbnail accessory")
	}

	return disgodiscord.NewSection(subs...).WithAccessory(accessory), nil
}

func parseV2GalleryItems(raw json.RawMessage) ([]disgodiscord.MediaGalleryItem, error) {
	if len(raw) == 0 {
		return nil, output.UsageError("v2 gallery block requires non-empty items")
	}
	var generic []any
	if err := json.Unmarshal(raw, &generic); err != nil || len(generic) == 0 {
		return nil, output.UsageError("v2 gallery block requires non-empty items array")
	}
	items := make([]disgodiscord.MediaGalleryItem, 0, len(generic))
	for _, entry := range generic {
		switch typed := entry.(type) {
		case string:
			items = append(items, disgodiscord.MediaGalleryItem{
				Media: disgodiscord.UnfurledMediaItem{URL: typed},
			})
		case map[string]any:
			urlStr, _ := typed["url"].(string)
			desc, _ := typed["description"].(string)
			if urlStr == "" {
				return nil, output.UsageError("v2 gallery item missing url")
			}
			item := disgodiscord.MediaGalleryItem{
				Media: disgodiscord.UnfurledMediaItem{URL: urlStr},
			}
			if desc != "" {
				item.Description = desc
			}
			items = append(items, item)
		default:
			return nil, output.UsageError("invalid v2 gallery item")
		}
	}
	return items, nil
}

// Engine manages live workflows and persistent dashboards inside `vox serve`
// as well as one-shot `vox interact` operations.
type Engine struct {
	restClient rest.Rest
	logger     *slog.Logger

	mutex          sync.Mutex
	workflows      map[string]*WorkflowState
	workflowDefs   map[string]WorkflowDefinition
	timeoutCancels map[string]context.CancelFunc

	dashboards     map[string]*DashboardState
	dashboardDefs  map[string]DashboardDefinition
	refreshCancels map[string]context.CancelFunc

	eventHandler func(map[string]any)
}

// NewEngine constructs an InteractEngine backed by restClient.
func NewEngine(restClient rest.Rest, logger *slog.Logger) *Engine {
	if logger == nil {
		logger = slog.Default()
	}
	return &Engine{
		restClient:     restClient,
		logger:         logger,
		workflows:      make(map[string]*WorkflowState),
		workflowDefs:   make(map[string]WorkflowDefinition),
		timeoutCancels: make(map[string]context.CancelFunc),
		dashboards:     make(map[string]*DashboardState),
		dashboardDefs:  make(map[string]DashboardDefinition),
		refreshCancels: make(map[string]context.CancelFunc),
	}
}

// SetEventHandler registers a callback invoked whenever workflow or dashboard events occur.
func (engine *Engine) SetEventHandler(handler func(map[string]any)) {
	engine.mutex.Lock()
	defer engine.mutex.Unlock()
	engine.eventHandler = handler
}

func (engine *Engine) emit(event map[string]any) {
	engine.mutex.Lock()
	handler := engine.eventHandler
	engine.mutex.Unlock()
	if handler != nil {
		handler(event)
	}
}

// WorkflowStart starts a multi-step workflow in channelID for userID and returns its key.
func (engine *Engine) WorkflowStart(channelID snowflake.ID, userID string, definition WorkflowDefinition) (string, error) {
	if len(definition.Steps) == 0 {
		return "", output.UsageError("workflow must contain at least one step")
	}
	if definition.WorkflowID == "" {
		definition.WorkflowID = uuid.NewString()[:8]
	}

	key := userID + ":" + definition.WorkflowID
	firstStep := definition.Steps[0]
	state := &WorkflowState{
		WorkflowID:    definition.WorkflowID,
		UserID:        userID,
		ChannelID:     channelID,
		CurrentStep:   firstStep.StepID,
		CollectedData: map[string]any{},
	}

	msgID, err := engine.sendWorkflowStep(channelID, definition.WorkflowID, firstStep)
	if err != nil {
		return "", err
	}
	state.MessageID = msgID

	engine.mutex.Lock()
	if prevCancel, ok := engine.timeoutCancels[key]; ok {
		prevCancel()
	}
	engine.workflows[key] = state
	engine.workflowDefs[key] = definition
	engine.startTimeoutLocked(key, firstStep.Timeout)
	engine.mutex.Unlock()

	return key, nil
}

// WorkflowCancel cancels an active workflow for userID and workflowID.
func (engine *Engine) WorkflowCancel(userID, workflowID string) bool {
	key := userID + ":" + workflowID
	engine.mutex.Lock()
	defer engine.mutex.Unlock()

	if cancel, ok := engine.timeoutCancels[key]; ok {
		cancel()
		delete(engine.timeoutCancels, key)
	}
	_, existed := engine.workflows[key]
	delete(engine.workflows, key)
	delete(engine.workflowDefs, key)
	return existed
}

// HandleWorkflowInteraction advances or completes a workflow from a component interaction.
// Returns an optional ephemeral response message when the caller should respond directly.
func (engine *Engine) HandleWorkflowInteraction(
	userID string,
	customID string,
	selectedValues []string,
) (reply string, deferred bool, err error) {
	parts := strings.SplitN(customID, ":", 4)
	if len(parts) < 4 || parts[0] != "wf" {
		return "Invalid workflow action.", false, nil
	}
	workflowID, stepID, action := parts[1], parts[2], parts[3]
	if action == "select" && len(selectedValues) > 0 {
		action = selectedValues[0]
	}

	key := userID + ":" + workflowID
	engine.mutex.Lock()
	state, ok := engine.workflows[key]
	definition := engine.workflowDefs[key]
	if !ok {
		engine.mutex.Unlock()
		return "No active workflow found.", false, nil
	}
	if cancel, exists := engine.timeoutCancels[key]; exists {
		cancel()
		delete(engine.timeoutCancels, key)
	}

	state.CollectedData[stepID] = action
	collectedCopy := maps.Clone(state.CollectedData)
	currentStep, idx, found := definition.Step(stepID)
	nextStepID := resolveNextStep(definition, currentStep, idx, found, action)

	if nextStepID == "" {
		delete(engine.workflows, key)
		delete(engine.workflowDefs, key)
		engine.mutex.Unlock()

		engine.emit(map[string]any{
			"event":          "workflow_step_completed",
			"key":            key,
			"step_id":        stepID,
			"action":         action,
			"collected_data": collectedCopy,
		})
		engine.emit(map[string]any{
			"event":          "workflow_finished",
			"key":            key,
			"collected_data": collectedCopy,
		})
		return "Workflow complete!", false, nil
	}

	nextStep, _, nextFound := definition.Step(nextStepID)
	if !nextFound {
		delete(engine.workflows, key)
		delete(engine.workflowDefs, key)
		engine.mutex.Unlock()
		return "Workflow complete!", false, nil
	}
	state.CurrentStep = nextStepID
	channelID := state.ChannelID
	engine.mutex.Unlock()

	engine.emit(map[string]any{
		"event":          "workflow_step_completed",
		"key":            key,
		"step_id":        stepID,
		"action":         action,
		"collected_data": collectedCopy,
	})

	msgID, sendErr := engine.sendWorkflowStep(channelID, workflowID, nextStep)
	if sendErr != nil {
		return "", true, sendErr
	}

	engine.mutex.Lock()
	if live, stillActive := engine.workflows[key]; stillActive {
		live.MessageID = msgID
		engine.startTimeoutLocked(key, nextStep.Timeout)
	}
	engine.mutex.Unlock()

	return "", true, nil
}

func resolveNextStep(def WorkflowDefinition, step WorkflowStep, idx int, found bool, action string) string {
	if !found {
		return ""
	}
	switch next := step.Next.(type) {
	case string:
		return next
	case map[string]any:
		if target, ok := next[action].(string); ok {
			return target
		}
		return ""
	default:
		if idx+1 < len(def.Steps) {
			return def.Steps[idx+1].StepID
		}
		return ""
	}
}

func (engine *Engine) startTimeoutLocked(key string, seconds int) {
	if seconds <= 0 {
		seconds = 300
	}
	ctx, cancel := context.WithCancel(context.Background())
	engine.timeoutCancels[key] = cancel
	go func() {
		defer cancel()
		timer := time.NewTimer(time.Duration(seconds) * time.Second)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			engine.mutex.Lock()
			_, stillActive := engine.workflows[key]
			delete(engine.workflows, key)
			delete(engine.workflowDefs, key)
			delete(engine.timeoutCancels, key)
			engine.mutex.Unlock()
			if stillActive {
				engine.emit(map[string]any{
					"event": "workflow_timeout",
					"key":   key,
				})
			}
		}
	}()
}

func (engine *Engine) sendWorkflowStep(channelID snowflake.ID, workflowID string, step WorkflowStep) (snowflake.ID, error) {
	prefix := fmt.Sprintf("wf:%s:%s:", workflowID, step.StepID)
	var rows []disgodiscord.LayoutComponent

	switch strings.ToLower(step.StepType) {
	case "confirm":
		rows = append(rows, disgodiscord.NewActionRow(
			disgodiscord.NewSuccessButton("Yes", prefix+"yes"),
			disgodiscord.NewDangerButton("No", prefix+"no"),
		))
	case "select":
		options := make([]disgodiscord.StringSelectMenuOption, 0, len(step.Components))
		for _, comp := range step.Components {
			val := comp.CustomID
			if val == "" {
				val = comp.Label
			}
			opt := disgodiscord.NewStringSelectMenuOption(comp.Label, val)
			if comp.Placeholder != "" {
				opt = opt.WithDescription(comp.Placeholder)
			}
			options = append(options, opt)
		}
		placeholder := step.Content
		if placeholder == "" {
			placeholder = "Select an option"
		}
		rows = append(rows, disgodiscord.NewActionRow(
			disgodiscord.NewStringSelectMenu(prefix+"select", placeholder, options...),
		))
	default:
		if len(step.Components) > 0 {
			buttons := make([]disgodiscord.InteractiveComponent, 0, len(step.Components))
			for _, comp := range step.Components {
				if comp.Type != "" && !strings.EqualFold(comp.Type, "button") {
					continue
				}
				btn, err := voxdiscord.BuildComponent(comp, prefix)
				if err != nil {
					return 0, err
				}
				buttons = append(buttons, btn)
			}
			if len(buttons) > 0 {
				rows = append(rows, disgodiscord.NewActionRow(buttons...))
			}
		}
	}

	msg, err := engine.restClient.CreateMessage(channelID, disgodiscord.MessageCreate{
		Content:    step.Content,
		Components: rows,
	})
	if err != nil {
		return 0, voxdiscord.MapError(err)
	}
	return msg.ID, nil
}

// DashboardCreate validates, sends, and registers a persistent dashboard.
func (engine *Engine) DashboardCreate(channelID snowflake.ID, definition DashboardDefinition) (string, error) {
	if definition.DashboardID == "" {
		definition.DashboardID = uuid.NewString()[:8]
	}
	if err := definition.Validate(); err != nil {
		return "", err
	}

	state := &DashboardState{
		DashboardID: definition.DashboardID,
		ChannelID:   channelID,
		CurrentPage: 0,
		Data:        map[string]any{},
	}

	if err := engine.renderDashboard(state, definition); err != nil {
		return "", err
	}

	engine.mutex.Lock()
	engine.dashboards[definition.DashboardID] = state
	engine.dashboardDefs[definition.DashboardID] = definition
	if definition.RefreshInterval > 0 {
		engine.startDashboardRefreshLocked(definition.DashboardID, definition.RefreshInterval)
	}
	engine.mutex.Unlock()

	return definition.DashboardID, nil
}

// DashboardUpdate merges updates into the dashboard's state and re-renders it.
func (engine *Engine) DashboardUpdate(dashboardID string, updates map[string]any) error {
	engine.mutex.Lock()
	state, ok := engine.dashboards[dashboardID]
	definition := engine.dashboardDefs[dashboardID]
	if !ok {
		engine.mutex.Unlock()
		return output.NewError("not_found", fmt.Sprintf("dashboard not found: %s", dashboardID))
	}
	maps.Copy(state.Data, updates)
	engine.mutex.Unlock()

	return engine.renderDashboard(state, definition)
}

// DashboardDelete stops periodic refresh, removes state, and deletes the Discord message.
func (engine *Engine) DashboardDelete(dashboardID string, channelID snowflake.ID) error {
	engine.mutex.Lock()
	if cancel, ok := engine.refreshCancels[dashboardID]; ok {
		cancel()
		delete(engine.refreshCancels, dashboardID)
	}
	state := engine.dashboards[dashboardID]
	delete(engine.dashboards, dashboardID)
	delete(engine.dashboardDefs, dashboardID)
	engine.mutex.Unlock()

	if state != nil && state.MessageID != 0 {
		targetChannel := state.ChannelID
		if targetChannel == 0 {
			targetChannel = channelID
		}
		if err := engine.restClient.DeleteMessage(targetChannel, state.MessageID); err != nil {
			return voxdiscord.MapError(err)
		}
	}
	return nil
}

// HandleDashboardInteraction processes dash:<id>:<action> button clicks.
func (engine *Engine) HandleDashboardInteraction(userID, customID string) (reply string, err error) {
	parts := strings.SplitN(customID, ":", 3)
	if len(parts) < 3 || parts[0] != "dash" {
		return "Invalid dashboard action.", nil
	}
	dashboardID, action := parts[1], parts[2]

	engine.mutex.Lock()
	state, ok := engine.dashboards[dashboardID]
	definition := engine.dashboardDefs[dashboardID]
	if !ok {
		engine.mutex.Unlock()
		return "Dashboard not found.", nil
	}

	switch action {
	case "prev":
		if state.CurrentPage > 0 {
			state.CurrentPage--
		}
		engine.mutex.Unlock()
		return "", engine.renderDashboard(state, definition)
	case "next":
		if state.CurrentPage < len(definition.Pages)-1 {
			state.CurrentPage++
		}
		engine.mutex.Unlock()
		return "", engine.renderDashboard(state, definition)
	case "counter":
		engine.mutex.Unlock()
		return "", nil
	default:
		dataCopy := maps.Clone(state.Data)
		engine.mutex.Unlock()
		engine.emit(map[string]any{
			"event":        "dashboard_interaction",
			"dashboard_id": dashboardID,
			"action":       action,
			"user_id":      userID,
			"data":         dataCopy,
		})
		return "", nil
	}
}

func (engine *Engine) startDashboardRefreshLocked(dashboardID string, intervalSeconds int) {
	ctx, cancel := context.WithCancel(context.Background())
	engine.refreshCancels[dashboardID] = cancel
	go func() {
		defer cancel()
		ticker := time.NewTicker(time.Duration(intervalSeconds) * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				engine.mutex.Lock()
				state, ok := engine.dashboards[dashboardID]
				definition := engine.dashboardDefs[dashboardID]
				engine.mutex.Unlock()
				if !ok {
					return
				}
				if err := engine.renderDashboard(state, definition); err != nil {
					engine.logger.Warn("dashboard refresh failed", "dashboard_id", dashboardID, "error", err)
				}
			}
		}
	}()
}

func (engine *Engine) renderDashboard(state *DashboardState, definition DashboardDefinition) error {
	page := definition.Pages[state.CurrentPage]
	nav := navButtons(definition.DashboardID, state.CurrentPage, len(definition.Pages))

	var createPayload disgodiscord.MessageCreate
	var updatePayload disgodiscord.MessageUpdate

	if definition.IsV2() {
		v2Components, err := BuildV2Components(page.Blocks, definition.DashboardID, nav)
		if err != nil {
			return err
		}
		flags := disgodiscord.MessageFlagIsComponentsV2
		createPayload = disgodiscord.MessageCreate{
			Components: v2Components,
			Flags:      flags,
		}
		updatePayload = disgodiscord.MessageUpdate{
			Components: &v2Components,
			Flags:      &flags,
		}
	} else {
		embed, err := buildDashboardEmbed(page.Embed)
		if err != nil {
			return err
		}
		components, err := buildDashboardEmbedComponents(definition, page, nav)
		if err != nil {
			return err
		}
		embeds := []disgodiscord.Embed{embed}
		createPayload = disgodiscord.MessageCreate{
			Embeds:     embeds,
			Components: components,
		}
		updatePayload = disgodiscord.MessageUpdate{
			Embeds:     &embeds,
			Components: &components,
		}
	}

	if state.MessageID != 0 {
		_, err := engine.restClient.UpdateMessage(state.ChannelID, state.MessageID, updatePayload)
		if err == nil {
			return nil
		}
		var restErr *rest.Error
		if !errors.As(err, &restErr) || restErr.Response == nil || restErr.Response.StatusCode != 404 {
			return voxdiscord.MapError(err)
		}
	}

	msg, err := engine.restClient.CreateMessage(state.ChannelID, createPayload)
	if err != nil {
		return voxdiscord.MapError(err)
	}
	state.MessageID = msg.ID
	return nil
}

func navButtons(dashboardID string, currentPage, totalPages int) []disgodiscord.InteractiveComponent {
	if totalPages <= 1 {
		return nil
	}
	prev := disgodiscord.NewSecondaryButton("Previous", fmt.Sprintf("dash:%s:prev", dashboardID))
	if currentPage == 0 {
		prev = prev.AsDisabled()
	}
	counter := disgodiscord.NewSecondaryButton(
		fmt.Sprintf("%d/%d", currentPage+1, totalPages),
		fmt.Sprintf("dash:%s:counter", dashboardID),
	).AsDisabled()
	next := disgodiscord.NewSecondaryButton("Next", fmt.Sprintf("dash:%s:next", dashboardID))
	if currentPage >= totalPages-1 {
		next = next.AsDisabled()
	}
	return []disgodiscord.InteractiveComponent{prev, counter, next}
}

func buildDashboardEmbed(raw map[string]any) (disgodiscord.Embed, error) {
	embed := disgodiscord.Embed{Color: 0x5865F2}
	if title, ok := raw["title"].(string); ok {
		embed.Title = title
	}
	if desc, ok := raw["description"].(string); ok {
		embed.Description = desc
	}
	if colorStr, ok := raw["color"].(string); ok && colorStr != "" {
		color, err := voxdiscord.ParseHexColor(colorStr)
		if err != nil {
			return disgodiscord.Embed{}, err
		}
		embed.Color = color
	} else if colorNum, ok := raw["color"].(float64); ok {
		embed.Color = int(colorNum)
	}
	for key, val := range raw {
		if key == "title" || key == "description" || key == "color" {
			continue
		}
		if text, ok := val.(string); ok && text != "" {
			embed.Footer = &disgodiscord.EmbedFooter{Text: text}
			break
		}
	}
	return embed, nil
}

func buildDashboardEmbedComponents(
	definition DashboardDefinition,
	page DashboardPage,
	nav []disgodiscord.InteractiveComponent,
) ([]disgodiscord.LayoutComponent, error) {
	prefix := fmt.Sprintf("dash:%s:", definition.DashboardID)
	var rows []disgodiscord.LayoutComponent
	if len(nav) > 0 {
		rows = append(rows, disgodiscord.NewActionRow(nav...))
	}

	combined := make([]voxdiscord.ComponentSpec, 0, len(page.Components)+len(definition.Components))
	combined = append(combined, page.Components...)
	combined = append(combined, definition.Components...)
	if len(combined) > 0 {
		items := make([]disgodiscord.InteractiveComponent, 0, len(combined))
		for _, spec := range combined {
			if spec.Type != "" && !strings.EqualFold(spec.Type, "button") {
				continue
			}
			btn, err := voxdiscord.BuildComponent(spec, prefix)
			if err != nil {
				return nil, err
			}
			items = append(items, btn)
		}
		if len(items) > 0 {
			rows = append(rows, disgodiscord.NewActionRow(items...))
		}
	}
	return rows, nil
}

// Close cancels all workflow timeout goroutines and dashboard refresh loops.
func (engine *Engine) Close() {
	engine.mutex.Lock()
	defer engine.mutex.Unlock()
	for _, cancel := range engine.timeoutCancels {
		cancel()
	}
	for _, cancel := range engine.refreshCancels {
		cancel()
	}
	clear(engine.workflows)
	clear(engine.workflowDefs)
	clear(engine.timeoutCancels)
	clear(engine.dashboards)
	clear(engine.dashboardDefs)
	clear(engine.refreshCancels)
}

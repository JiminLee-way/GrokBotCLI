# Box gateway method reference

Interface reference for interoperability, compiled from the request/response shapes the Grok Bot desktop
client uses. Call any of these with `gb call <method> '{json}'` or `POST /v1/call` on gbd.

Call shape: `POST {gatewayUrl}/api/<method>` · JSON body · `Authorization: Bearer <gatewayToken>` · `x-anyrun-network-token: <networkToken>`.
**Bold** fields are required, `?` marks optional ones. Unknown fields are dropped; `null` is rejected for optional fields that are not nullable.

- Client methods: 167 · host-only (not callable by clients): 40 · event channels: 27

## Bots & groups

| method | args | reply |
|---|---|---|
| `confirmCloudAgentPublishProposal` | **agentId**: string<br>**bcId**: string<br>**proposalId**: string<br>requireConfirmationForUpdates?: boolean\|null<br>**slug**: string<br>visibility?: "private"\|"public"\|"team"\|null | SandCloudAgentPublishDecision |
| `countAgents` | {} | number |
| `createAgent` | avatarColor?: string<br>avatarPngBase64?: string<br>avatarShape?: string<br>clientNonce?: string<br>creationRoute?: object \| object \| null<br>**description**: string<br>harness?: "box"\|"temporal"\|null<br>isAutomatic?: boolean\|null<br>isIntroductionSuppressed?: boolean\|null<br>isKickstartRequested?: boolean\|null<br>language?: string<br>**name**: string<br>origin?: "dev"\|"user"\|null<br>purpose?: "disk-saver"\|"plugin-auth"\|null<br>supportsTemporalHarness?: boolean\|null<br>templateId?: string<br>title?: string | SandCreateAgentResult |
| `createAgentAutomation` | **id**: string<br>**spec**: SandAutomationSpec | SandAutomation[] |
| `createAgentFromTemplate` | **agentId**: string<br>**avatarColor**: string<br>**avatarShape**: string<br>creatorContext?: string<br>**expectedActiveVersion**: number<br>importEntry?: "chat_link"<br>isAutomatic?: boolean\|null<br>language?: string<br>**name**: string<br>**shareId**: string | SandCreateAgentFromTemplateResult |
| `createAgentWorkflow` | **id**: string<br>**spec**: SandSkillSpec | SandSkill[] |
| `createGroup` | clientNonce?: string<br>creationRoute?: object \| object \| null<br>description?: string<br>humanMemberUserIds?: number[]<br>**memberAgentIds**: string[]<br>**name**: string<br>namedBy?: "app"\|"user"\|null | SandCreateAgentResult |
| `deleteAgentAutomation` | **automationId**: string<br>**id**: string | SandAutomation[] |
| `deleteAgentWorkflow` | **id**: string<br>**workflowId**: string | SandSkill[] |
| `deleteAgents` | **ids**: string[] | SandDeleteAgentResult |
| `dismissCloudAgentPublishProposal` | **agentId**: string<br>**bcId**: string<br>**proposalId**: string | SandCloudAgentPublishDecision |
| `duplicateAgent` | **id**: string | SandCreateAgentResult |
| `ensureCloudAgentArtifacts` | **agentId**: string<br>**bcId**: string | SandCloudAgentArtifactSync[] |
| `generateAgentAvatarImage` | **description**: string | SandGeneratedAvatarImage |
| `getAgentAutomations` | **id**: string | SandAutomation[] |
| `getAgentAvatar` | **id**: string | SandAgentAvatarResult |
| `getAgentChannels` | **id**: string | SandChannelsView |
| `getAgentNotificationAvatar` | **id**: string | SandAgentNotificationAvatar |
| `getAgentTasks` | **agentId**: string | SandAgentTask[] |
| `getAgentThread` | **id**: string<br>**rootId**: string | SandAgentThread |
| `getAgentTodos` | **id**: string | SandAgentTodo[] |
| `getAgentTranscript` | **id**: string | SandTranscriptEntry[] |
| `getAgentTranscriptPage` | beforeSeq?: number<br>**id**: string<br>**limit**: number<br>sinceMs?: number<br>**untilMs**: number | SandRetainedTranscriptPage |
| `getAgentTranscriptTail` | beforeSeq?: number<br>**id**: string<br>**limit**: number<br>sessionId?: string<br>unreadBoundaryAt?: number | SandTranscriptPage |
| `getAgentTranscriptWindow` | beforeSeq?: number<br>**id**: string<br>**limit**: number | SandTranscriptWindow |
| `getAgentWorkflows` | **id**: string | SandSkill[] |
| `getBotTemplateForSourceAgent` | **sourceAgentId**: string | RegressionEraBotTemplateWireView \| SandBotTemplateWireView \| null |
| `getCloudAgentConversation` | **bcId**: string | SandCloudAgentReadFailure \| object \| object \| null |
| `getCloudAgentInfo` | **bcId**: string<br>includeArtifacts?: boolean\|null<br>includeFiles?: boolean\|null | SandCloudAgentInfo \| null |
| `getCloudAgentPublishProposals` | **bcId**: string | SandCloudAgentPublishProposalsRead |
| `getSubagents` | **id**: string | SandSubagentInfo[] |
| `getVoiceCall` | **callId**: string<br>**id**: string | SandVoiceCallWireRecord \| null |
| `importAgentWorkflowText` | **id**: string<br>**markdown**: string<br>name?: string | SandSkillImportEvent |
| `importAgentWorkflowUrl` | **id**: string<br>name?: string<br>**url**: string | SandSkillImportEvent |
| `interruptAgentRun` | **id**: string<br>sessionId?: string | SandInterruptAgentRunResult |
| `kickstartAgent` | **id**: string | SandKickstartAgentResult \| null |
| `listAgents` | {} | SandAgentSummary[] |
| `listCloudAgentPublishedApps` | {} | SandCloudAgentPublishedAppsRead |
| `markAgentTaskDone` | **agentId**: string<br>**taskId**: string | SandAgentTask[] |
| `markAgentTaskRead` | **agentId**: string<br>**entryId**: string<br>**taskId**: string | SandAgentTask[] |
| `nudgeVoiceCall` | **callId**: string<br>**id**: string<br>**request**: string<br>**spokenTurns**: string[] | VoiceCallNudgeReply |
| `openAgent` | **id**: string | SandTranscriptEntry[] |
| `openAgentTail` | **id**: string<br>**limit**: number<br>sessionId?: string<br>unreadBoundaryAt?: number | SandTranscriptPage |
| `openAgentWindowed` | **id**: string<br>**limit**: number | SandTranscriptWindow |
| `previewVoice` | **greetingId**: string<br>**voiceId**: string | SandVoicePreviewAudio |
| `readMainAgentContext` | **id**: string | SandMainAgentContext |
| `readVoiceCallSentMessages` | **id**: string | SandOutlineWrittenMessage[] |
| `recordVoiceCall` | **record**: SandVoiceCallWireRecord | null |
| `runAgentAutomationNow` | **automationId**: string<br>**id**: string<br>sessionId?: string | null |
| `runAgentWorkflowNow` | **id**: string<br>**workflowId**: string | null |
| `searchAgents` | limit?: number<br>**query**: string | SandMessageMatch[] |
| `setAgentAutomationEnabled` | **automationId**: string<br>**id**: string<br>**isEnabled**: boolean | SandAutomation[] |
| `setAgentAvatarBytes` | **id**: string<br>**pngBase64**: null \| string | SandAgentSummary \| null |
| `setAgentDraftingDisabled` | **id**: string<br>**isDisabled**: boolean | null |
| `setAgentHiddenFromSidebar` | **id**: string<br>**isHidden**: boolean | null |
| `setAgentNotifyOnUpdates` | **id**: string<br>**isEnabled**: boolean | null |
| `setAgentUnread` | atMs?: number<br>**id**: string<br>**isUnread**: boolean | null |
| `setAgentVoice` | **id**: string<br>voiceId?: null \| string<br>voiceLanguage?: string<br>voiceSpeed?: number | null |
| `setGroupMembers` | **id**: string<br>**memberAgentIds**: string[]<br>requesterAgentId?: string | SandAgentSummary \| null |
| `setVoiceCallPresence` | acceptsOverheard?: boolean\|null<br>**callId**: string<br>**id**: string<br>**isOnTheLine**: boolean | null |
| `stopCloudAgent` | **bcId**: string | null |
| `updateAgent` | **id**: string<br>**profile**: SandAgentProfile | SandAgentSummary \| null |
| `updateAgentAutomation` | **automationId**: string<br>**id**: string<br>**spec**: SandAutomationSpec | SandAutomation[] |
| `updateAgentWorkflow` | **id**: string<br>**spec**: SandSkillSpec<br>**workflowId**: string | SandSkill[] |
| `updateCloudAgentPublishApp` | **apex**: string<br>**bcId**: string<br>muteRedeployPrompts?: boolean\|null<br>requireConfirmationForUpdates?: boolean\|null<br>**slug**: string<br>visibility?: "private"\|"public"\|"team"\|null | SandCloudAgentPublishAppUpdate |

## Messages & transcript

| method | args | reply |
|---|---|---|
| `carryBoxSecretsToBot` | **names**: string[]<br>**serverId**: string | GrokBotSecretCarryOverResult[] |
| `discardDraft` | **agentId**: string<br>**entryId**: string<br>sessionId?: string | SandWidgetAnswerResult |
| `disconnectListenerPlatform` | **platform**: string | null |
| `dismissUserForm` | **agentId**: string<br>**entryId**: string<br>**mode**: "dismissed"\|"escalated"<br>platform?: "android"\|"desktop"\|"ios"\|null | null |
| `dismissWidget` | **agentId**: string<br>**entryId**: string | SandWidgetAnswerResult |
| `getBoxSecretsStatus` | {} | SandBoxSecretsStatus |
| `getConversationOutline` | **id**: string | SandConversationOutlineItem[] |
| `promptAcceptanceStatus` | **accountSlot**: string<br>agentId?: string<br>**clientNonce**: string<br>sessionId?: string | PromptAcceptanceLookup |
| `reactToMessage` | **agentId**: string<br>**emoji**: string<br>**entryId**: string | null |
| `readAttachmentChunk` | agentId?: string<br>**length**: number<br>**offset**: number<br>**path**: string<br>videoPlayback?: boolean\|null | SandAttachmentChunk \| null |
| `readAttachmentImage` | fit?: string<br>**path**: string<br>rendition?: "original"\|"preview"\|null<br>tile?: string | ResolvedImageAttachment \| null |
| `readAttachmentText` | agentId?: string<br>**path**: string | object \| object \| null |
| `respondToWidget` | **agentId**: string<br>**entryId**: string<br>**value**: string | SandWidgetAnswerResult \| null |
| `rollBackConversation` | **id**: string<br>sessionId?: string | null |
| `searchMedia` | limit?: number<br>**query**: string | SandMediaMatch[] |
| `seedConversationName` | **id**: string<br>**prompt**: string | SandConversationNameSeedOutcome |
| `sendDraft` | **agentId**: string<br>**draft**: SandDraftPayload<br>**entryId**: string<br>sessionId?: string<br>shownSignatureHtml?: string | SandWidgetAnswerResult |
| `sendPrompt` | **agentId**: string<br>attachmentNames?: string[]<br>attachmentPaths?: string[]<br>automationWriteProvenance?: "template_import"\|"untrusted"\|null<br>clientNonce?: string<br>composedAtMs?: number<br>directAddressedAcceptance?: true<br>enterEpochMs?: number<br>initiator?: "card_action"\|"plugin_setup"\|"teach_recording"\|"template_import"\|"template_share"\|"user"\|…<br>isFork?: boolean\|null<br>machineId?: string<br>mcpConfigJson?: string<br>onePasswordAppInstalled?: boolean\|null<br>**prompt**: string<br>replyToId?: string<br>richText?: string<br>sendSource?: "action_button_dictation"\|"app_intent"\|"chat"\|"push"\|"unknown"\|null<br>sessionId?: string<br>source?: "desktop"\|"mobile"\|null<br>taskId?: string<br>traceparent?: string | SandSendPromptResult \| null |
| `setBoxSecrets` | **secrets**: object | SandBoxSecretsStatus |
| `startFreshConversation` | **id**: string<br>sessionId?: string | null |
| `storeSecret` | **agentId**: string<br>**target**: object \| object<br>**value**: string | boolean |
| `submitSecret` | **agentId**: string<br>**entryId**: string<br>sessionId?: string<br>**value**: string | null |
| `submitUserForm` | **agentId**: string<br>**entryId**: string<br>platform?: "android"\|"desktop"\|"ios"\|null<br>saveToVault?: boolean\|null<br>**values**: object | null |
| `uploadAttachment` | agentId?: string<br>**bytesBase64**: string<br>**filename**: string | SandUploadAttachmentResult |
| `uploadAttachmentChunk` | agentId?: string<br>**bytesBase64**: string<br>**filename**: string<br>**offset**: number<br>**totalSize**: number<br>**uploadId**: string | SandUploadAttachmentChunkResult |
| `voteFeedback` | **action**: "down"\|"revert"\|"submit"\|"up"<br>**agentId**: string<br>categories?: string[]<br>comment?: string<br>**entryId**: string | null |

## Approvals & grants

| method | args | reply |
|---|---|---|
| `awaitCookieOriginApproval` | **requestId**: string<br>**waitMs**: number | SandCookieOriginApprovalGatewayReply |
| `awaitMessagesGrants` | **requestId**: string<br>**waitMs**: number | SandMessagesGrantsGatewayReply |
| `cancelCookieOriginApproval` | **requestId**: string | null |
| `cancelMessagesGrants` | **requestId**: string | null |
| `requestCookieOriginApproval` | agentId?: string<br>**origins**: CookieOriginRequestWireEntry[]<br>**requestId**: string | SandCookieOriginApprovalGatewayReply |
| `requestMessagesGrants` | **grants**: "automation"\|"contacts"\|"fullDiskAccess"[]<br>**requestId**: string | SandMessagesGrantsGatewayReply |
| `resolveAutoReviewApproval` | **agentId**: string<br>approvalPlatform?: "android"\|"desktop"\|"ios"\|null<br>approvedCommand?: string<br>**entryId**: string<br>meetingJoin?: SandMeetingJoinAnswer<br>**requestId**: string<br>**resolution**: "always"\|"approved"\|"denied"<br>sessionId?: string | null |
| `resolveConnectorGrant` | **agentId**: string<br>**entryId**: string<br>**requestId**: string<br>**resolution**: "allowed"\|"always_all_team_bots"\|"always_this_bot"\|"skipped"<br>sessionId?: string | null |
| `resolveLocalToolPermission` | **agentId**: string<br>**entryId**: string<br>**requestId**: string<br>**resolution**: "allow-once"\|"always"\|"deny"\|"never"<br>sessionId?: string | null |
| `resolveMessagesGrants` | **requestId**: string | null |
| `resolveVirtualCardApproval` | **agentId**: string<br>attestation?: SandSpendApprovalAttestation<br>**entryId**: string<br>failureReason?: string<br>paymentMethodId?: string<br>**requestId**: string<br>**resolution**: "approved"\|"denied"\|"expired"\|"failed"<br>spendRequestId?: string | null |

## Skills, workflows & automations

| method | args | reply |
|---|---|---|
| `getAsyncTasks` | **id**: string | SandAsyncTaskInfo[] |
| `getAutomationWebhookCredential` | **automationId**: string<br>**id**: string | SandAutomationWebhookCredential |
| `getEffectiveMcpPlugins` | {} | SandEffectivePlugin[] |
| `getMcpPluginLogo` | **url**: string | object \| null |
| `getPluginSyncStatus` | {} | SandPluginSyncStatus |
| `getSkillPublishTargets` | {} | SandSkillPublishTargets |
| `hasRestorablePluginListingCopy` | **pluginId**: string | boolean |
| `listAllAutomations` | {} | SandScheduledAutomation[] |
| `publishSkill` | **teamId**: number<br>**workflowId**: string | SandSkillPublishResult |
| `resyncPublishedSkill` | **workflowId**: string | SandSkillPublishResult |
| `skillsCatalog` | {} | SandSkillCatalogEntry[] |
| `syncPluginSkills` | {} | SandPluginSkillInfo[] |
| `uninstallMcpPlugin` | **pluginId**: string | SandMcpRemoveResult |
| `unpublishPluginListing` | **pluginId**: string<br>**teamId**: number | SandSkillUnpublishResult |
| `unpublishSkill` | **workflowId**: string | SandSkillUnpublishResult |
| `updateMcpPluginInstall` | **pluginId**: string<br>**values**: object | SandMcpState |

## Apps (MCP), channels & integrations

| method | args | reply |
|---|---|---|
| `authenticateMcpServer` | accountKey?: string<br>forceReauth?: boolean\|null<br>oauthRedirectUri?: string<br>requestingAgentId?: string<br>**serverId**: string<br>trigger?: "connector_card" | SandMcpAuthResult |
| `callMcpAppTool` | agentId?: string<br>**argumentsJson**: string<br>**serverIdentifier**: string<br>**toolName**: string<br>**userActivated**: boolean | SandMcpAppToolCallResult |
| `completeGithubConnect` | code?: string<br>error?: string<br>installationId?: string<br>setupAction?: string<br>**state**: string | SandGithubConnectCompletion |
| `completeMcpOAuth` | **code**: string<br>**state**: string | null |
| `connectChannel` | **id**: string<br>**platform**: string<br>**token**: string | SandChannelsView |
| `disconnectChannel` | **id**: string<br>**platform**: string | SandChannelsView |
| `getListenerConnectUrl` | forceOauth?: boolean\|null<br>oauthRedirectUri?: string<br>**platform**: string | {url} |
| `getListenerIntegrations` | {} | SandListenerIntegrationsView |
| `getMcpCatalog` | {} | SandMcpCatalogEntryView[] |
| `getMcpState` | {} | SandMcpState |
| `installMcpEntry` | **entryId**: string<br>hasTeamConfiguredVariables?: boolean\|null<br>values?: object | SandMcpState |
| `listBoxMcpServers` | **serverIdentifiers**: string[] | {servers} |
| `listMcpServerTools` | **serverId**: string | SandMcpServerTool[] |
| `readMcpAppResource` | agentId?: string<br>documentSha256?: string<br>entryId?: string<br>**resourceUri**: string<br>**serverIdentifier**: string<br>sessionId?: string | SandMcpAppDocumentResult |
| `refreshChannel` | **id**: string<br>**platform**: string | SandChannelsView |
| `refreshMcp` | completion?: SandMcpAuthCompletion | null |
| `removeMcpAccount` | **accountKey**: string<br>**serverId**: string | SandMcpState |
| `removeMcpServer` | **serverId**: string | SandMcpRemoveResult |
| `renameMcpAccount` | **accountKey**: string<br>**newAccountKey**: string<br>**serverId**: string | SandMcpState |
| `setMcpCustomInstructions` | **instructions**: string<br>**serverId**: string | SandMcpState |
| `toggleMcpToolDisabled` | **serverId**: string<br>**toolName**: string | SandMcpServerTool[] |

## Cloud agents & templates

| method | args | reply |
|---|---|---|
| `deleteBotTemplate` | **shareId**: string | null |
| `getBotTemplateExportPolicy` | {} | SandBotTemplateExportPolicyReply |
| `getBotTemplateVersion` | **shareId**: string<br>**version**: number | SandBotTemplateGatewayView |
| `listBotTemplates` | {} | SandBotTemplateGatewayView[] |
| `publishBotTemplate` | **shareId**: string<br>**version**: number | SandBotTemplateGatewayView |
| `setBotTemplateVisibility` | **shareId**: string<br>**visibility**: "public"\|"team" | SandBotTemplateView |

## Box & host

| method | args | reply |
|---|---|---|
| `clearTrays` | {} | null |
| `dismissTray` | **id**: string | null |
| `ensureForeverBox` | **id**: string | SandForeverBoxStatus \| null |
| `getForeverBoxStatus` | **id**: string | SandForeverBoxStatus \| null |
| `getHostSettings` | {} | SandHostSettings |
| `getHostStatus` | includeManagedCapabilities?: boolean\|null | SandHostStatus |
| `getTeachRecordingStatus` | {} | SandTeachRecordingStatus |
| `getTrays` | {} | SandErrorTray[] |
| `handBackForeverBox` | **id**: string<br>requestId?: string<br>trigger?: "button"\|"dismissed"\|"viewer-closed"\|null | null |
| `isEgressTunnelAvailable` | {} | boolean |
| `requestDiskSaverAudit` | **id**: string | SandDiskSaverAuditResult \| null |
| `setHostSettings` | autoReviewInstructions?: SandAutoReviewInstructions<br>computerUseModel?: SandModelSelection \| null<br>experimentOverrides?: object<br>featureFlagOverrides?: object<br>hasSeenOnboarding?: boolean\|null<br>localToolPermission?: "always"\|"ask"\|"never"\|null<br>localToolPermissionByMachineId?: object<br>localToolPermissionMachineId?: string<br>mcpBoxServers?: string[]<br>mcpCustomInstructions?: object<br>mcpCustomInstructionsAccountScope?: null \| string<br>mcpCustomInstructionsByServerId?: object<br>mcpDisabledToolsByServerId?: object<br>messagesEnabled?: boolean\|null<br>notifications?: SandNotificationConfigInput<br>osLocale?: string<br>pinnedAgentIds?: string[]<br>selectedTeam?: SandSelectedTeamBinding \| null<br>sidebarSections?: SidebarSectionUpdate[]<br>toolPermissions?: SandToolPermissions<br>userTimeZone?: string<br>userTimeZoneOverride?: string<br>webauthnProxyEnabled?: boolean\|null | SandHostSettings |
| `startTeachRecording` | **agentId**: string<br>**entryPoint**: "composer_menu"\|"fullscreen_title_bar"\|"screen_hover" | SandTeachRecordingStatus |
| `stopTeachRecording` | **agentId**: string<br>**save**: boolean | SandTeachRecordingStatus |
| `updateForeverBox` | **id**: string | SandForeverBoxStatus |
| `updateHostNow` | force?: boolean\|null<br>includeErrorDetail?: boolean\|null | SandHostUpdateResult |

## Other

| method | args | reply |
|---|---|---|
| `clearGeneratedRoomNameStamps` | **rooms**: object[] | GeneratedRoomNameStampCleanupOutcome |
| `injectChromeCookies` | **cookies**: ChromeCookieRecord[] | {chromeDebugPorts, injected, monitorsReached, sites} |
| `isGlobalSearchEnabled` | {} | boolean |
| `requestWebAuthnCeremony` | **kind**: "create"\|"get"<br>**optionsJson**: string<br>**origin**: string | SandWebAuthnCeremonyResult |
| `setWindowFocused` | **isFocused**: boolean<br>surface?: "desktop"\|"mobile"\|null | null |
| `transcribeAudio` | **audioBase64**: string<br>language?: string<br>**mimeType**: string | SandVoiceTranscription |

## Host-only methods (box internal, not callable by clients)

`acquireCredentialFillAgentHold`, `appendConnectorCard`, `assignAgentToSidebarSection`, `autoUpdateBoxNow`, `awaitBoxSecretsApplied`, `awaitBrokeredCredentialAccessRequest`, `cancelRoomMemberTurn`, `clearBoxStoreNow`, `createBrokeredCredentialAccessRequest`, `deleteAgent`, `deliverAgentMessage`, `deliverBrokeredCredentialSession`, `deliverRoomMemberTurnResult`, `exportGrokBotWorkingState`, `fillBrokeredCredential`, `fillBrowserCredential`, `fillBrowserCredentialDirect`, `fillBrowserOneTimeCode`, `fillBrowserPasswordStep`, `getBoxStoreStatus`, `getHarnessMigrationWindow`, `getNavigationTelemetryIdentity`, `getOpenCredentialRequest`, `getPauseState`, `listPromotableRooms`, `listSidebarSections`, `markAgentRequestEnded`, `markAgentRequestStarted`, `prepareBoxForRecreate`, `readVoiceCallAgentContext`, `reconcileAgentIdentity`, `releaseCredentialFillAgentHold`, `resolveCredentialBrowserTarget`, `resolveCredentialRequest`, `resumeBoxAfterRecreate`, `runRoomMemberTurn`, `setBoxMigrating`, `setHttpProxyName`, `snapshotBoxStoreNow`, `syncUserSecrets`

## Event channels (`GET {gatewayUrl}/events`, `data: {channel, payload}`)

| channel | payload |
|---|---|
| `agent-activity` | **agentId**: string<br>**client**: SandAgentClientOverlay \| null<br>**live**: SandAgentLiveOverlay \| null |
| `agent-meetings` | **agentId**: string |
| `agent-tasks` | **agentId**: string |
| `agent-upserted` | **activeAgentId**: string<br>**agent**: SandAgentSummary<br>ordered?: OrderedStamp |
| `agents` | **activeAgentId**: string<br>**agents**: SandAgentSummary[]<br>coverage?: object \| object \| null<br>ordered?: OrderedStamp |
| `async-tasks` | **parentAgentId**: string<br>**tasks**: SandAsyncTaskInfo[] |
| `automations` | **agentId**: string<br>**automations**: SandAutomation[] |
| `bot-secrets` | **agentId**: string |
| `bot-skills` | **agentId**: string |
| `box-disk-pressure` | SandBoxDiskPressure \| null |
| `cloud-agent-update` | **bcId**: string<br>**status**: "creating"\|"error"\|"expired"\|"finished"\|"running"\|"unknown"<br>**updatedAtMs**: number |
| `computer-action` | **agentId**: string<br>button?: string<br>count?: number<br>**type**: SandComputerCursorAction<br>**x**: number<br>**y**: number |
| `forever-box` | **agentId**: string<br>diskPressure?: SandBoxDiskPressure<br>handoff?: SandBoxHandoffInfo \| null<br>hostUpdateAvailable?: boolean\|null<br>hostVersion?: null \| string<br>imageUpdateAvailable?: boolean\|null<br>pull?: SandBoxPullStatus \| null<br>**state**: SandBoxRunState<br>**vncUrl**: null \| string<br>windows?: SandForeverBoxWindow[] |
| `host-settings` | **fields**: SandHostSettingsField[] |
| `mcp-auth` | **accountKey**: string<br>failureDetail?: string<br>outcome?: "cancelled"\|"connected"\|null<br>**requestingAgentId**: null \| string<br>**serverId**: string<br>serverIdentifier?: string<br>**serverName**: string<br>serverUrl?: string |
| `mcp-servers` | **servers**: object[] |
| `messages-grants` | **grants**: "automation"\|"contacts"\|"fullDiskAccess"[]<br>**requestId**: string |
| `outline` | SandConversationOutlineEvent |
| `server-page-title-changed` | **atMs**: number<br>**page**: SandPageTitleIdentity<br>**title**: string |
| `server-roster-changed` | SandServerRosterChange |
| `subagents` | **parentAgentId**: string<br>**subagents**: SandSubagentInfo[] |
| `teach-recording` | **agentId**: null \| string<br>**maxDurationMs**: number<br>**startedAtMs**: null \| number<br>**state**: "idle"\|"recording" |
| `todos` | **agentId**: string<br>**todos**: SandAgentTodo[] |
| `transcript` | object \| object \| object \| object \| object |
| `tray` | TrayEvent |
| `voice-call-nudge` | **agentId**: string<br>**callId**: string<br>**kind**: VoiceCallNudgeKind<br>**update**: string |
| `workflows` | **agentId**: string<br>**workflows**: SandSkill[] |

## failureCodes

`agent-rename/refused`, `auto-review/stale`, `automation-run-now/refused`, `bot-template/not-found`, `bot_template_import_access_denied`, `box-handoff/stale`, `chrome-cookie-import-unavailable`, `connector-grant/stale`, `gateway/unknown-method`, `generate-image-refused`, `mcp-config-refused`, `scm-connect/refused`, `secret-request/save-refused`, `secret-request/save-unconfirmed`, `send/agent-no-access`, `send/agent-unavailable`, `send/message-too-long`, `send/nonce-digest-mismatch`, `skill-publish/refused`, `user-form/stale`, `virtual-card/stale`

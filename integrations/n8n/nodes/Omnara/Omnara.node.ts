import {
	IExecuteFunctions,
	INodeExecutionData,
	INodeType,
	INodeTypeDescription,
	NodeConnectionType,
	NodeOperationError,
	IDataObject,
} from 'n8n-workflow';

import { omnaraApiRequest, validateUUID } from './GenericFunctions';
import { randomUUID } from 'crypto';

export class Omnara implements INodeType {
	description: INodeTypeDescription = {
		displayName: 'Omnara',
		name: 'omnara',
		icon: 'file:omnara.png',
		group: ['communication'],
		version: 1,
		subtitle: '={{$parameter["operation"]}}',
		description: 'Send messages to Omnara for human-in-the-loop workflows',
		defaults: {
			name: 'Omnara',
		},
		inputs: [NodeConnectionType.Main],
		outputs: [NodeConnectionType.Main],
		usableAsTool: true,
		credentials: [
			{
				name: 'omnaraApi',
				required: true,
			},
		],
		properties: [
			{
				displayName: 'Operation',
				name: 'operation',
				type: 'options',
				noDataExpression: true,
				options: [
					{
						name: 'Send Message',
						value: 'sendMessage',
						description: 'Send a message to Omnara',
						action: 'Send a message to omnara',
					},
					{
						name: 'Send Message (Wait Ready)',
						value: 'sendMessageWaitReady',
						description: 'Send a message prepared for use with Wait node',
						action: 'Send a message prepared for wait node',
					},
					{
						name: 'End Session',
						value: 'endSession',
						description: 'End an agent session',
						action: 'End an agent session',
					},
				],
				default: 'sendMessage',
			},
			// Send Message fields
			{
				displayName: 'Message Content',
				name: 'content',
				type: 'string',
				typeOptions: {
					rows: 4,
				},
				displayOptions: {
					show: {
						operation: ['sendMessage', 'sendMessageWaitReady'],
					},
				},
				default: '',
				required: true,
				description: 'The message content to send',
			},
			{
				displayName: 'Agent Instance ID',
				name: 'agentInstanceId',
				type: 'string',
				displayOptions: {
					show: {
						operation: ['sendMessage', 'sendMessageWaitReady'],
					},
				},
				default: '',
				description: 'Existing agent instance ID (leave empty to create new)',
			},
			{
				displayName: 'Agent Type',
				name: 'agentType',
				type: 'string',
				displayOptions: {
					show: {
						operation: ['sendMessage', 'sendMessageWaitReady'],
						agentInstanceId: [''],
					},
				},
				default: 'n8n-workflow',
				description: 'Type of agent (required when creating new instance)',
			},
			// Webhook URL for Wait Ready operation
			{
				displayName: 'Webhook URL',
				name: 'webhookUrl',
				type: 'string',
				displayOptions: {
					show: {
						operation: ['sendMessageWaitReady'],
					},
				},
				default: '',
				description: 'Webhook URL from Wait node (use expression: {{$node["Wait"].json["$resumeWebhookUrl"]}})',
				placeholder: '{{$node["Wait"].json["$resumeWebhookUrl"]}}',
			},
			// End Session fields
			{
				displayName: 'Agent Instance ID',
				name: 'endSessionInstanceId',
				type: 'string',
				displayOptions: {
					show: {
						operation: ['endSession'],
					},
				},
				default: '',
				required: true,
				description: 'The agent instance ID to end',
			},
			// Additional Options
			{
				displayName: 'Additional Options',
				name: 'additionalOptions',
				type: 'collection',
				placeholder: 'Add Option',
				default: {},
				displayOptions: {
					show: {
						operation: ['sendMessage', 'sendMessageWaitReady'],
					},
				},
				options: [
					{
						displayName: 'Git Diff',
						name: 'gitDiff',
						type: 'string',
						typeOptions: {
							rows: 10,
						},
						default: '',
						description: 'Git diff content to include with the message',
					},
					{
						displayName: 'Send Email',
						name: 'sendEmail',
						type: 'boolean',
						default: false,
						description: 'Whether to send email notification',
					},
					{
						displayName: 'Send Push',
						name: 'sendPush',
						type: 'boolean',
						default: false,
						description: 'Whether to send push notification',
					},
					{
						displayName: 'Send SMS',
						name: 'sendSms',
						type: 'boolean',
						default: false,
						description: 'Whether to send SMS notification',
					},
					{
						displayName: 'Requires User Input',
						name: 'requiresUserInput',
						type: 'boolean',
						default: false,
						description: 'Whether this message requires user input (for polling, not webhook)',
					},
				],
			},
		],
	};

	async execute(this: IExecuteFunctions): Promise<INodeExecutionData[][]> {
		const items = this.getInputData();
		const returnData: INodeExecutionData[] = [];
		const operation = this.getNodeParameter('operation', 0) as string;

		for (let i = 0; i < items.length; i++) {
			try {
				if (operation === 'sendMessage' || operation === 'sendMessageWaitReady') {
					const content = this.getNodeParameter('content', i) as string;
					let agentInstanceId = this.getNodeParameter('agentInstanceId', i) as string;
					const additionalOptions = this.getNodeParameter('additionalOptions', i) as IDataObject;

					// Prepare request body
					const body: IDataObject = {
						content,
						requires_user_input: additionalOptions.requiresUserInput || false,
					};

					// Handle agent instance ID
					if (agentInstanceId) {
						if (!validateUUID(agentInstanceId)) {
							throw new NodeOperationError(this.getNode(), 'Invalid agent instance ID format');
						}
						body.agent_instance_id = agentInstanceId;
					} else {
						// Generate new instance ID and include agent type
						const agentType = this.getNodeParameter('agentType', i) as string;
						if (!agentType) {
							throw new NodeOperationError(this.getNode(), 'Agent type is required when creating new instance');
						}
						body.agent_instance_id = randomUUID();
						body.agent_type = agentType;
					}

					// Add optional fields
					if (additionalOptions.gitDiff) {
						body.git_diff = additionalOptions.gitDiff;
					}
					if (additionalOptions.sendEmail !== undefined) {
						body.send_email = additionalOptions.sendEmail;
					}
					if (additionalOptions.sendPush !== undefined) {
						body.send_push = additionalOptions.sendPush;
					}
					if (additionalOptions.sendSms !== undefined) {
						body.send_sms = additionalOptions.sendSms;
					}

					// For Wait Ready operation, add webhook URL
					if (operation === 'sendMessageWaitReady') {
						const webhookUrl = this.getNodeParameter('webhookUrl', i) as string;
						if (webhookUrl) {
							body.webhook_url = webhookUrl;
						}
					}

					// Make API request
					const response = await omnaraApiRequest.call(
						this,
						'POST',
						'/api/v1/messages/agent',
						body,
					);

					// Format response
					const resultData: IDataObject = {
						success: response.success,
						messageId: response.message_id,
						agentInstanceId: response.agent_instance_id,
					};

					// Include queued messages if any
					if (response.queued_user_messages && response.queued_user_messages.length > 0) {
						resultData.queuedUserMessages = response.queued_user_messages;
					}

					// For Wait Ready, include webhook info
					if (operation === 'sendMessageWaitReady' && body.webhook_url) {
						resultData.webhookUrl = body.webhook_url;
						resultData.waitNodeReady = true;
					}

					returnData.push({
						json: resultData,
						pairedItem: i,
					});

				} else if (operation === 'endSession') {
					const agentInstanceId = this.getNodeParameter('endSessionInstanceId', i) as string;

					if (!agentInstanceId) {
						throw new NodeOperationError(this.getNode(), 'Agent instance ID is required');
					}

					if (!validateUUID(agentInstanceId)) {
						throw new NodeOperationError(this.getNode(), 'Invalid agent instance ID format');
					}

					const body: IDataObject = {
						agent_instance_id: agentInstanceId,
					};

					const response = await omnaraApiRequest.call(
						this,
						'POST',
						'/api/v1/sessions/end',
						body,
					);

					returnData.push({
						json: {
							success: response.success,
							agentInstanceId: response.agent_instance_id,
							finalStatus: response.final_status,
						},
						pairedItem: i,
					});
				}
			} catch (error) {
				if (this.continueOnFail()) {
					const errorMessage = error instanceof Error ? error.message : 'Unknown error occurred';
					returnData.push({
						json: {
							error: errorMessage,
						},
						pairedItem: i,
					});
					continue;
				}
				throw error;
			}
		}

		return [returnData];
	}
}
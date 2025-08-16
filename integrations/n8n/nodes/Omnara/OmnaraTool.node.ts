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

export class OmnaraTool implements INodeType {
	description: INodeTypeDescription = {
		displayName: 'Omnara Tool',
		name: 'omnaraTool',
		icon: 'file:omnara.png',
		group: ['transform'],
		version: 1,
		subtitle: '={{$parameter["operation"]}}',
		description: 'Omnara tools for AI Agents - Send messages and get human feedback',
		defaults: {
			name: 'Omnara Tool',
		},
		inputs: [NodeConnectionType.AiTool],
		outputs: [NodeConnectionType.AiTool],
		outputNames: ['Tool'],
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
						description: 'Send a message to Omnara dashboard',
						action: 'Send a message to omnara',
					},
					{
						name: 'Ask for Human Input',
						value: 'askHuman',
						description: 'Ask for human input and wait for response',
						action: 'Ask for human input',
					},
					{
						name: 'End Session',
						value: 'endSession',
						description: 'End the current agent session',
						action: 'End an agent session',
					},
				],
				default: 'sendMessage',
			},
			{
				displayName: 'Description for AI',
				name: 'toolDescription',
				type: 'string',
				default: '',
				placeholder: 'e.g., Use this to notify the human about progress',
				description: 'Explain to the AI when to use this tool',
				displayOptions: {
					show: {
						operation: ['sendMessage', 'askHuman'],
					},
				},
			},
			// Dynamic parameters based on operation
			{
				displayName: 'Message Content',
				name: 'content',
				type: 'string',
				typeOptions: {
					rows: 4,
				},
				displayOptions: {
					show: {
						operation: ['sendMessage', 'askHuman'],
					},
				},
				default: '={{$json.content}}',
				required: true,
				description: 'The message or question to send',
			},
			{
				displayName: 'Agent Instance ID',
				name: 'agentInstanceId',
				type: 'string',
				displayOptions: {
					show: {
						operation: ['sendMessage', 'askHuman', 'endSession'],
					},
				},
				default: '={{$json.agentInstanceId}}',
				description: 'Agent instance ID (AI will manage this)',
			},
			{
				displayName: 'Additional Options',
				name: 'additionalOptions',
				type: 'collection',
				placeholder: 'Add Option',
				default: {},
				displayOptions: {
					show: {
						operation: ['sendMessage', 'askHuman'],
					},
				},
				options: [
					{
						displayName: 'Agent Type',
						name: 'agentType',
						type: 'string',
						default: 'n8n-ai-agent',
						description: 'Type of agent for tracking',
					},
					{
						displayName: 'Send Notifications',
						name: 'sendNotifications',
						type: 'boolean',
						default: false,
						description: 'Whether to send push/email/SMS notifications',
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
				if (operation === 'sendMessage') {
					const content = this.getNodeParameter('content', i) as string;
					let agentInstanceId = this.getNodeParameter('agentInstanceId', i) as string;
					const additionalOptions = this.getNodeParameter('additionalOptions', i) as IDataObject;

					// Prepare request body
					const body: IDataObject = {
						content,
						requires_user_input: false,
					};

					// Handle agent instance ID
					if (agentInstanceId && agentInstanceId !== '') {
						if (!validateUUID(agentInstanceId)) {
							// Generate new if invalid
							agentInstanceId = randomUUID();
						}
						body.agent_instance_id = agentInstanceId;
					} else {
						// Generate new instance ID
						body.agent_instance_id = randomUUID();
						body.agent_type = (additionalOptions.agentType as string) || 'n8n-ai-agent';
					}

					// Add notification settings
					if (additionalOptions.sendNotifications) {
						body.send_push = true;
						body.send_email = true;
					}

					// Make API request
					const response = await omnaraApiRequest.call(
						this,
						'POST',
						'/api/v1/messages/agent',
						body,
					);

					returnData.push({
						json: {
							success: response.success,
							messageId: response.message_id,
							agentInstanceId: response.agent_instance_id,
							queuedUserMessages: response.queued_user_messages,
							// Include original for AI context
							operation: 'messageSent',
							content: content,
						},
						pairedItem: i,
					});

				} else if (operation === 'askHuman') {
					const content = this.getNodeParameter('content', i) as string;
					let agentInstanceId = this.getNodeParameter('agentInstanceId', i) as string;
					const additionalOptions = this.getNodeParameter('additionalOptions', i) as IDataObject;

					// For asking human, we need to handle this differently
					// This would typically integrate with a wait mechanism
					const body: IDataObject = {
						content,
						requires_user_input: true,
					};

					// Handle agent instance ID
					if (agentInstanceId && agentInstanceId !== '') {
						if (!validateUUID(agentInstanceId)) {
							agentInstanceId = randomUUID();
						}
						body.agent_instance_id = agentInstanceId;
					} else {
						body.agent_instance_id = randomUUID();
						body.agent_type = (additionalOptions.agentType as string) || 'n8n-ai-agent';
					}

					// Add notification settings
					if (additionalOptions.sendNotifications) {
						body.send_push = true;
						body.send_email = true;
					}

					// Make API request
					const response = await omnaraApiRequest.call(
						this,
						'POST',
						'/api/v1/messages/agent',
						body,
					);

					// For AI agent tools, we might want to indicate that this needs follow-up
					returnData.push({
						json: {
							success: response.success,
							messageId: response.message_id,
							agentInstanceId: response.agent_instance_id,
							operation: 'waitingForHuman',
							question: content,
							queuedUserMessages: response.queued_user_messages,
							// Signal to AI that it should wait or check back
							requiresFollowUp: true,
						},
						pairedItem: i,
					});

				} else if (operation === 'endSession') {
					const agentInstanceId = this.getNodeParameter('agentInstanceId', i) as string;

					if (!agentInstanceId || agentInstanceId === '') {
						throw new NodeOperationError(this.getNode(), 'Agent instance ID is required to end session');
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
							operation: 'sessionEnded',
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
							operation: 'error',
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
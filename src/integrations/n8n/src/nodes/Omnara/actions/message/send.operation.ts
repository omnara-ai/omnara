import {
	IExecuteFunctions,
	INodeExecutionData,
	INodeProperties,
	NodeOperationError,
} from 'n8n-workflow';

import { omnaraApiRequest, formatMessageResponse, generateAgentInstanceId, getAgentType } from '../../../../utils/GenericFunctions';

export const sendMessageDescription: INodeProperties[] = [
	{
		displayName: 'Agent Instance ID',
		name: 'agentInstanceId',
		type: 'string',
		default: '',
		required: false,
		displayOptions: {
			show: {
				resource: ['message'],
				operation: ['send'],
			},
		},
		placeholder: 'e.g. 550e8400-e29b-41d4-a716-446655440000',
		description:
			'A unique UUID for this workflow run. Must be the same across all Omnara nodes in this workflow. Use webhook data or generate with {{ $uuid() }}. If left empty, a default value will be generated.',
	},
	{
		displayName: 'Agent Type',
		name: 'agentType',
		type: 'string',
		default: '',
		required: false,
		displayOptions: {
			show: {
				resource: ['message'],
				operation: ['send'],
			},
		},
		placeholder: 'e.g. Customer Support',
		description:
			'The name of your agent on Omnara dashboard. Must be the same across all Omnara nodes. If left empty, the workflow name will be used as the default.',
	},
	{
		displayName: 'Message',
		name: 'message',
		type: 'string',
		default: '',
		required: true,
		typeOptions: {
			rows: 4,
		},
		displayOptions: {
			show: {
				resource: ['message'],
				operation: ['send'],
			},
		},
		placeholder: 'e.g. Build completed successfully',
		description:
			'Status update or informational message to send to the user. This will appear in their Omnara web/mobile app. The workflow will continue immediately without waiting for a response.',
	},
	{
		displayName: 'Additional Options',
		name: 'additionalOptions',
		type: 'collection',
		placeholder: 'Add Option',
		default: {},
		displayOptions: {
			show: {
				resource: ['message'],
				operation: ['send'],
			},
		},
		options: [
			{
				displayName: 'Send Email Notification',
				name: 'sendEmail',
				type: 'boolean',
				default: false,
				description: 'Whether to send an email notification',
			},
			{
				displayName: 'Send SMS Notification',
				name: 'sendSms',
				type: 'boolean',
				default: false,
				description: 'Whether to send an SMS notification',
			},
			{
				displayName: 'Send Push Notification',
				name: 'sendPush',
				type: 'boolean',
				default: false,
				description: 'Whether to send a push notification',
			},
		],
	},
];

export async function execute(
	this: IExecuteFunctions,
	index: number,
): Promise<INodeExecutionData[]> {

	const agentType = getAgentType.call(this, 0);
	const agentInstanceId = generateAgentInstanceId.call(this, 0, agentType);

	const message = this.getNodeParameter('message', index) as string;
	const additionalOptions = this.getNodeParameter('additionalOptions', index, {}) as any;

	if (!message) {
		throw new NodeOperationError(this.getNode(), 'Message is required', { itemIndex: index });
	}

	const body: any = {
		agent_instance_id: agentInstanceId,
		agent_type: agentType,
		content: message,
		requires_user_input: false,
	};
	if (additionalOptions.sendEmail !== undefined) {
		body.send_email = additionalOptions.sendEmail;
	}
	if (additionalOptions.sendSms !== undefined) {
		body.send_sms = additionalOptions.sendSms;
	}
	if (additionalOptions.sendPush !== undefined) {
		body.send_push = additionalOptions.sendPush;
	}

	try {
		const response = await omnaraApiRequest.call(this, 'POST', '/messages/agent', body);

		const result = {
			success: response.success,
			agentInstanceId: response.agent_instance_id,
			messageId: response.message_id,
			queuedUserMessages: response.queued_user_messages
				? response.queued_user_messages.map(formatMessageResponse)
				: [],
		};

		return [{ json: result }];
	} catch (error) {
		throw new NodeOperationError(
			this.getNode(),
			`Failed to send message: ${error instanceof Error ? error.message : String(error)}`,
			{ itemIndex: index },
		);
	}
}

import {
	INodeType,
	INodeTypeDescription,
	IWebhookFunctions,
	IWebhookResponseData,
	IDataObject,
	NodeConnectionType,
} from 'n8n-workflow';

export class OmnaraWebhook implements INodeType {
	description: INodeTypeDescription = {
		displayName: 'Omnara Webhook',
		name: 'omnaraWebhook',
		icon: 'file:omnara.svg',
		group: ['trigger'],
		version: 1,
		description: 'Receive responses from Omnara when users respond to messages',
		defaults: {
			name: 'Omnara Webhook',
		},
		inputs: [],
		outputs: [NodeConnectionType.Main],
		webhooks: [
			{
				name: 'default',
				httpMethod: 'POST',
				responseMode: 'onReceived',
				path: 'omnara',
			},
		],
		properties: [
			{
				displayName: 'Info',
				name: 'info',
				type: 'notice',
				default: '',
				description: 'This webhook receives user responses from Omnara. When you send a message with "Send and Wait", configure Omnara to send responses to this webhook URL.',
			},
		],
	};

	async webhook(this: IWebhookFunctions): Promise<IWebhookResponseData> {
		const req = this.getRequestObject();
		const resp = this.getResponseObject();
		const body = this.getBodyData() as IDataObject;
		const headers = this.getHeaderData() as IDataObject;

		// Log the webhook for debugging
		console.log('Omnara webhook received:', { body, headers });

		// Send success response
		resp.status(200).json({
			success: true,
			message: 'Webhook received successfully',
		});

		// Return the user response data
		return {
			workflowData: [
				[
					{
						json: {
							userResponse: body.user_message || body.content || '',
							userId: body.user_id,
							messageId: body.message_id,
							agentInstanceId: body.agent_instance_id,
							timestamp: body.timestamp || new Date().toISOString(),
							metadata: body.metadata || {},
							headers,
						},
					},
				],
			],
		};
	}
}
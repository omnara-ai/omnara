import {
	IExecuteFunctions,
	INodeExecutionData,
	INodeType,
	INodeTypeDescription,
	NodeOperationError,
	NodeConnectionType,
	IWebhookFunctions,
	IWebhookResponseData,
	IDataObject,
	SEND_AND_WAIT_OPERATION,
} from 'n8n-workflow';

import * as message from './actions/message';
import * as session from './actions/session';
import { omnaraSendAndWaitWebhook } from '../../utils/sendAndWaitWebhook';
import { sendAndWaitWebhooksDescription } from '../../utils/sendAndWait/descriptions';
import { configureWaitTillDate } from '../../utils/sendAndWait/configureWaitTillDate';

export class Omnara implements INodeType {
	webhook = omnaraSendAndWaitWebhook;
	
	description: INodeTypeDescription = {
		displayName: 'Omnara',
		name: 'omnara',
		icon: 'file:omnara.svg',
		group: ['transform'],
		version: 1,
		subtitle: '={{$parameter["operation"] + ": " + $parameter["resource"]}}',
		description: 'Interact with Omnara AI agents',
		defaults: {
			name: 'Omnara',
		},
		inputs: [NodeConnectionType.Main],
		outputs: [NodeConnectionType.Main],
		webhooks: sendAndWaitWebhooksDescription,
		credentials: [
			{
				name: 'omnaraApiV2',
				required: true,
			},
		],
		properties: [
			{
				displayName: 'Resource',
				name: 'resource',
				type: 'options',
				noDataExpression: true,
				options: [
					{
						name: 'Message',
						value: 'message',
						description: 'Send messages to Omnara agents',
					},
					{
						name: 'Session',
						value: 'session',
						description: 'Manage agent sessions',
					},
				],
				default: 'message',
			},
			...message.descriptions,
			...session.descriptions,
		],
		usableAsTool: true,
	};

	async execute(this: IExecuteFunctions): Promise<INodeExecutionData[][]> {
		const items = this.getInputData();
		const returnData: INodeExecutionData[] = [];
		const resource = this.getNodeParameter('resource', 0) as string;
		const operation = this.getNodeParameter('operation', 0) as string;

		for (let i = 0; i < items.length; i++) {
			try {
				let responseData: INodeExecutionData[] = [];

				if (resource === 'message') {
					if (operation === 'send') {
						responseData = await message.send.execute.call(this, i);
					} else if (operation === 'sendAndWait') {
						// Send the message to Omnara
						await message.sendAndWait.execute.call(this, items);
						
						// Configure how long to wait
						const waitTill = configureWaitTillDate(this, 0);
						
						// Put the execution on hold until the webhook is called
						// Only wait if a valid date is returned
						if (waitTill instanceof Date) {
							await this.putExecutionToWait(waitTill);
						} else if (waitTill === 'WAIT_INDEFINITELY') {
							// Wait indefinitely (no timeout)
							await this.putExecutionToWait(undefined as any);
						}
						
						// Return the input data (will be replaced with webhook response when resumed)
						return [this.getInputData()];
					} else {
						throw new NodeOperationError(
							this.getNode(),
							`The operation "${operation}" is not supported for resource "${resource}"`,
							{ itemIndex: i },
						);
					}
				} else if (resource === 'session') {
					if (operation === 'end') {
						responseData = await session.end.execute.call(this, i);
					} else {
						throw new NodeOperationError(
							this.getNode(),
							`The operation "${operation}" is not supported for resource "${resource}"`,
							{ itemIndex: i },
						);
					}
				} else {
					throw new NodeOperationError(
						this.getNode(),
						`The resource "${resource}" is not supported`,
						{ itemIndex: i },
					);
				}

				returnData.push(...responseData);
			} catch (error) {
				if (this.continueOnFail()) {
					returnData.push({ 
						json: { 
							error: error instanceof Error ? error.message : String(error),
							resource,
							operation,
							itemIndex: i,
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
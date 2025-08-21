import * as send from './send.operation';
import * as sendAndWait from './sendAndWait.operation';

import { INodeProperties } from 'n8n-workflow';

export { send, sendAndWait };

export const descriptions: INodeProperties[] = [
	{
		displayName: 'Operation',
		name: 'operation',
		type: 'options',
		noDataExpression: true,
		displayOptions: {
			show: {
				resource: ['message'],
			},
		},
		options: [
			{
				name: 'Send',
				value: 'send',
				description: 'Send a message to an Omnara agent',
				action: 'Send a message',
			},
			{
				name: 'Send and Wait',
				value: 'sendAndWait',
				description: 'Send a message and wait for user response',
				action: 'Send a message and wait for response',
			},
		],
		default: 'send',
	},
	...send.sendMessageDescription,
	...sendAndWait.sendAndWaitDescription,
];
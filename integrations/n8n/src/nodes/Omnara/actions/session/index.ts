import * as end from './end.operation';

import { INodeProperties } from 'n8n-workflow';

export { end };

export const descriptions: INodeProperties[] = [
	{
		displayName: 'Operation',
		name: 'operation',
		type: 'options',
		noDataExpression: true,
		displayOptions: {
			show: {
				resource: ['session'],
			},
		},
		options: [
			{
				name: 'End',
				value: 'end',
				description: 'End an agent session',
				action: 'End agent session',
			},
		],
		default: 'end',
	},
	...end.endSessionDescription,
];

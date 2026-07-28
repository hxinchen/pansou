'use strict';

var assert = require('node:assert/strict');
var sse = require('./sse.js');

var events = [];
var parser = sse.createParser(function (event) { events.push(event); });
parser.feed('id: 7\nevent: activity\nda');
parser.feed('ta: {"active_run":null}\n\n: heart');
parser.feed('beat\n\nid: 8\r\nevent: counters\r\ndata: {"resource_count":2}\r\n\r\n');

assert.deepEqual(events, [
  { id: '7', name: 'activity', data: '{"active_run":null}', comment: false },
  { id: '', name: 'message', data: '', comment: true },
  { id: '8', name: 'counters', data: '{"resource_count":2}', comment: false }
]);

var multiline = sse.parseBlock('event: status\ndata: {"state":\ndata: "healthy"}');
assert.equal(multiline.data, '{"state":\n"healthy"}');

(function (root, factory) {
  'use strict';
  var api = factory();
  if (typeof module === 'object' && module.exports) module.exports = api;
  if (root) root.PanSouSSE = api;
}(typeof window !== 'undefined' ? window : null, function () {
  'use strict';

  function parseBlock(block) {
    var event = { id: '', name: 'message', data: '', comment: false };
    var data = [];
    String(block || '').split(/\r?\n/).forEach(function (line) {
      if (!line) return;
      if (line.charAt(0) === ':') {
        event.comment = true;
        return;
      }
      var separator = line.indexOf(':');
      var field = separator < 0 ? line : line.slice(0, separator);
      var value = separator < 0 ? '' : line.slice(separator + 1).replace(/^ /, '');
      if (field === 'id') event.id = value;
      else if (field === 'event') event.name = value || 'message';
      else if (field === 'data') data.push(value);
    });
    event.data = data.join('\n');
    return event;
  }

  function createParser(onBlock) {
    var buffer = '';
    return {
      feed: function (chunk) {
        buffer += String(chunk || '');
        var boundary;
        while ((boundary = buffer.search(/\r?\n\r?\n/)) >= 0) {
          var match = buffer.slice(boundary).match(/^\r?\n\r?\n/)[0];
          var block = buffer.slice(0, boundary);
          buffer = buffer.slice(boundary + match.length);
          if (block) onBlock(parseBlock(block));
        }
      },
      end: function () {
        if (buffer.trim()) onBlock(parseBlock(buffer));
        buffer = '';
      }
    };
  }

  return { createParser: createParser, parseBlock: parseBlock };
}));

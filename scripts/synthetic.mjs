// Original synthetic content only. The PDF is generated with exact xref offsets.
export function syntheticPdf(title = 'CWA Synthetic PDF Compatibility') {
  const content = 'BT /F1 12 Tf 20 100 Td (Original synthetic CWA test content) Tj ET\n';
  const objects = [
    '<< /Type /Catalog /Pages 2 0 R >>',
    '<< /Type /Pages /Kids [3 0 R] /Count 1 >>',
    '<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>',
    '<< /Length ' + Buffer.byteLength(content) + ' >>\nstream\n' + content + 'endstream',
    '<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>',
    '<< /Title (' + title.replace(/[()\\]/g, '_') + ') /Author (Test Author) >>',
  ];
  let text = '%PDF-1.4\n';
  const offsets = [0];
  for (let n = 0; n < objects.length; n++) {
    offsets.push(Buffer.byteLength(text));
    text += n + 1 + ' 0 obj\n' + objects[n] + '\nendobj\n';
  }
  const start = Buffer.byteLength(text);
  text += 'xref\n0 ' + (objects.length + 1) + '\n0000000000 65535 f \n';
  text += offsets
    .slice(1)
    .map((n) => String(n).padStart(10, '0') + ' 00000 n \n')
    .join('');
  text +=
    'trailer\n<< /Size ' +
    (objects.length + 1) +
    ' /Root 1 0 R /Info 6 0 R >>\nstartxref\n' +
    start +
    '\n%%EOF\n';
  return Buffer.from(text);
}

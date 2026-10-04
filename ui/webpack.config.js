const path = require('path');

// Argo CD loads every /tmp/extensions/**/extension*.js file and exposes React
// as a global, so React must NOT be bundled (see Argo CD UI extensions docs).
module.exports = {
  entry: './src/index.tsx',
  output: {
    path: path.resolve(__dirname, 'dist', 'resources', 'zea'),
    filename: 'extension-zea.js',
    clean: true,
  },
  resolve: {
    extensions: ['.ts', '.tsx', '.js'],
  },
  externals: {
    react: 'React',
  },
  module: {
    rules: [
      {
        test: /\.tsx?$/,
        use: 'ts-loader',
        exclude: /node_modules/,
      },
    ],
  },
  performance: {
    hints: false,
  },
  devtool: false,
};

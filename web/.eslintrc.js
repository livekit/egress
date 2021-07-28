module.exports = {
  extends: ['airbnb-typescript'],
  parserOptions: {
    project: './tsconfig.eslint.json',
  },
  rules: {
    'no-console': 'off',
    'import/export': 'off',
    'max-classes-per-file': 'off',
    'no-param-reassign': 'off',
    'no-await-in-loop': 'off',
    'consistent-return': 'off',
    'class-methods-use-this': 'off',
    'react/jsx-props-no-spreading': 'off',
    '@typescript-eslint/no-use-before-define': 'off',
  },
};

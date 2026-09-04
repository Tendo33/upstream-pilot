package app

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
)

const maxManagedRate = 100000.0

func calculateManagedRate(mode string, offset float64, expression string, current float64, rates []float64) (float64, error) {
	valid := make([]float64, 0, len(rates))
	for _, rate := range rates {
		if isFinite(rate) && rate >= 0 {
			valid = append(valid, rate)
		}
	}
	if len(valid) == 0 {
		return 0, fmt.Errorf("没有可用的绑定账号倍率")
	}
	if !isFinite(offset) {
		return 0, fmt.Errorf("偏移必须是有效数字")
	}

	var base float64
	switch mode {
	case "first":
		base = valid[0]
	case "average":
		base = rateSum(valid) / float64(len(valid))
	case "min":
		base = rateMin(valid)
	case "max":
		base = rateMax(valid)
	case "custom":
		expression = strings.TrimSpace(expression)
		if expression == "" {
			return 0, fmt.Errorf("请输入自定义公式")
		}
		value, err := newRateExpression(expression, current, valid).parse()
		if err != nil {
			return 0, err
		}
		base = value
	default:
		return 0, fmt.Errorf("不支持的倍率规则 %q", mode)
	}

	result := math.Round((base+offset)*10000) / 10000
	if !isFinite(result) || result <= 0 || result > maxManagedRate {
		return 0, fmt.Errorf("规则结果必须大于 0 且不超过 %.0f", maxManagedRate)
	}
	return result, nil
}

func rateSum(values []float64) float64 {
	var result float64
	for _, value := range values {
		result += value
	}
	return result
}

func rateMin(values []float64) float64 {
	result := values[0]
	for _, value := range values[1:] {
		result = math.Min(result, value)
	}
	return result
}

func rateMax(values []float64) float64 {
	result := values[0]
	for _, value := range values[1:] {
		result = math.Max(result, value)
	}
	return result
}

type rateTokenKind int

const (
	rateTokenEOF rateTokenKind = iota
	rateTokenNumber
	rateTokenIdentifier
	rateTokenPlus
	rateTokenMinus
	rateTokenMultiply
	rateTokenDivide
	rateTokenModulo
	rateTokenLeftParen
	rateTokenRightParen
	rateTokenComma
)

type rateToken struct {
	kind  rateTokenKind
	text  string
	value float64
	pos   int
}

type rateExpression struct {
	input        string
	current      float64
	rates        []float64
	tokens       []rateToken
	index        int
	validateOnly bool
}

func newRateExpression(input string, current float64, rates []float64) *rateExpression {
	return &rateExpression{input: input, current: current, rates: rates}
}

func validateRateExpression(input string) error {
	parser := &rateExpression{input: input, current: 1, rates: []float64{1}, validateOnly: true}
	_, err := parser.parse()
	return err
}

func (p *rateExpression) parse() (float64, error) {
	tokens, err := lexRateExpression(p.input)
	if err != nil {
		return 0, err
	}
	p.tokens = tokens
	value, err := p.parseAdditive()
	if err != nil {
		return 0, err
	}
	if token := p.peek(); token.kind != rateTokenEOF {
		return 0, fmt.Errorf("公式第 %d 个字符附近存在多余内容", token.pos+1)
	}
	if !p.validateOnly && !isFinite(value) {
		return 0, fmt.Errorf("公式计算结果不是有效数字")
	}
	return value, nil
}

func lexRateExpression(input string) ([]rateToken, error) {
	tokens := make([]rateToken, 0, len(input)/2+1)
	for index := 0; index < len(input); {
		character := rune(input[index])
		if unicode.IsSpace(character) {
			index++
			continue
		}
		start := index
		if (character >= '0' && character <= '9') || character == '.' {
			seenDigit := false
			seenDot := false
			for index < len(input) {
				value := input[index]
				if value >= '0' && value <= '9' {
					seenDigit = true
					index++
					continue
				}
				if value == '.' && !seenDot {
					seenDot = true
					index++
					continue
				}
				break
			}
			if !seenDigit {
				return nil, fmt.Errorf("公式第 %d 个字符不是有效数字", start+1)
			}
			text := input[start:index]
			value, err := strconv.ParseFloat(text, 64)
			if err != nil || !isFinite(value) {
				return nil, fmt.Errorf("公式第 %d 个字符附近的数字无效", start+1)
			}
			tokens = append(tokens, rateToken{kind: rateTokenNumber, text: text, value: value, pos: start})
			continue
		}
		if unicode.IsLetter(character) || character == '_' {
			index++
			for index < len(input) {
				value := rune(input[index])
				if !unicode.IsLetter(value) && !unicode.IsDigit(value) && value != '_' {
					break
				}
				index++
			}
			tokens = append(tokens, rateToken{kind: rateTokenIdentifier, text: strings.ToLower(input[start:index]), pos: start})
			continue
		}
		kind := rateTokenEOF
		switch input[index] {
		case '+':
			kind = rateTokenPlus
		case '-':
			kind = rateTokenMinus
		case '*':
			kind = rateTokenMultiply
		case '/':
			kind = rateTokenDivide
		case '%':
			kind = rateTokenModulo
		case '(':
			kind = rateTokenLeftParen
		case ')':
			kind = rateTokenRightParen
		case ',':
			kind = rateTokenComma
		default:
			return nil, fmt.Errorf("公式第 %d 个字符 %q 不受支持", start+1, input[index:start+1])
		}
		tokens = append(tokens, rateToken{kind: kind, text: input[index : index+1], pos: index})
		index++
	}
	tokens = append(tokens, rateToken{kind: rateTokenEOF, pos: len(input)})
	return tokens, nil
}

func (p *rateExpression) parseAdditive() (float64, error) {
	left, err := p.parseMultiplicative()
	if err != nil {
		return 0, err
	}
	for {
		token := p.peek()
		if token.kind != rateTokenPlus && token.kind != rateTokenMinus {
			return left, nil
		}
		p.index++
		right, err := p.parseMultiplicative()
		if err != nil {
			return 0, err
		}
		if token.kind == rateTokenPlus {
			left += right
		} else {
			left -= right
		}
		if p.validateOnly {
			left = 1
		}
	}
}

func (p *rateExpression) parseMultiplicative() (float64, error) {
	left, err := p.parseUnary()
	if err != nil {
		return 0, err
	}
	for {
		token := p.peek()
		if token.kind != rateTokenMultiply && token.kind != rateTokenDivide && token.kind != rateTokenModulo {
			return left, nil
		}
		p.index++
		right, err := p.parseUnary()
		if err != nil {
			return 0, err
		}
		if !p.validateOnly && (token.kind == rateTokenDivide || token.kind == rateTokenModulo) && right == 0 {
			return 0, fmt.Errorf("公式不能除以 0")
		}
		if p.validateOnly {
			left = 1
			continue
		}
		switch token.kind {
		case rateTokenMultiply:
			left *= right
		case rateTokenDivide:
			left /= right
		case rateTokenModulo:
			left = math.Mod(left, right)
		}
	}
}

func (p *rateExpression) parseUnary() (float64, error) {
	token := p.peek()
	if token.kind == rateTokenPlus || token.kind == rateTokenMinus {
		p.index++
		value, err := p.parseUnary()
		if err != nil {
			return 0, err
		}
		if token.kind == rateTokenMinus {
			return -value, nil
		}
		return value, nil
	}
	return p.parsePrimary()
}

func (p *rateExpression) parsePrimary() (float64, error) {
	token := p.peek()
	switch token.kind {
	case rateTokenNumber:
		p.index++
		return token.value, nil
	case rateTokenIdentifier:
		p.index++
		if p.peek().kind == rateTokenLeftParen {
			return p.parseFunction(token)
		}
		return p.variable(token)
	case rateTokenLeftParen:
		p.index++
		value, err := p.parseAdditive()
		if err != nil {
			return 0, err
		}
		if p.peek().kind != rateTokenRightParen {
			return 0, fmt.Errorf("公式第 %d 个字符附近缺少右括号", token.pos+1)
		}
		p.index++
		return value, nil
	default:
		return 0, fmt.Errorf("公式第 %d 个字符附近缺少数字、变量或函数", token.pos+1)
	}
}

func (p *rateExpression) variable(token rateToken) (float64, error) {
	switch token.text {
	case "avg":
		if p.validateOnly {
			return 1, nil
		}
		return rateSum(p.rates) / float64(len(p.rates)), nil
	case "first":
		if p.validateOnly {
			return 1, nil
		}
		return p.rates[0], nil
	case "current":
		return p.current, nil
	case "count":
		return float64(len(p.rates)), nil
	}
	if strings.HasPrefix(token.text, "r") && len(token.text) > 1 {
		index, err := strconv.Atoi(token.text[1:])
		if err == nil && index >= 0 {
			return p.rateAt(index), nil
		}
	}
	return 0, fmt.Errorf("公式变量 %q 不受支持", token.text)
}

func (p *rateExpression) parseFunction(name rateToken) (float64, error) {
	p.index++ // left parenthesis
	arguments := make([]float64, 0, 3)
	if p.peek().kind != rateTokenRightParen {
		for {
			value, err := p.parseAdditive()
			if err != nil {
				return 0, err
			}
			arguments = append(arguments, value)
			if p.peek().kind != rateTokenComma {
				break
			}
			p.index++
		}
	}
	if p.peek().kind != rateTokenRightParen {
		return 0, fmt.Errorf("函数 %s 缺少右括号", name.text)
	}
	p.index++
	return p.callFunction(name.text, arguments)
}

func (p *rateExpression) callFunction(name string, arguments []float64) (float64, error) {
	switch name {
	case "min", "max", "sum":
		if p.validateOnly {
			return 1, nil
		}
		values := arguments
		if len(values) == 0 {
			values = p.rates
		}
		if len(values) == 0 {
			return 0, fmt.Errorf("函数 %s 没有可计算的倍率", name)
		}
		switch name {
		case "min":
			return rateMin(values), nil
		case "max":
			return rateMax(values), nil
		default:
			return rateSum(values), nil
		}
	case "abs", "floor", "ceil":
		if len(arguments) != 1 {
			return 0, fmt.Errorf("函数 %s 需要 1 个参数", name)
		}
		if p.validateOnly {
			return 1, nil
		}
		switch name {
		case "abs":
			return math.Abs(arguments[0]), nil
		case "floor":
			return math.Floor(arguments[0]), nil
		default:
			return math.Ceil(arguments[0]), nil
		}
	case "round":
		if len(arguments) < 1 || len(arguments) > 2 {
			return 0, fmt.Errorf("函数 round 需要 1 或 2 个参数")
		}
		if p.validateOnly {
			return 1, nil
		}
		places := 4
		if len(arguments) == 2 {
			places = int(arguments[1])
			if arguments[1] != float64(places) || places < 0 || places > 12 {
				return 0, fmt.Errorf("round 的小数位必须是 0 至 12 的整数")
			}
		}
		factor := math.Pow10(places)
		return math.Round(arguments[0]*factor) / factor, nil
	case "clamp":
		if len(arguments) != 3 {
			return 0, fmt.Errorf("函数 clamp 需要 3 个参数")
		}
		if p.validateOnly {
			return 1, nil
		}
		if arguments[1] > arguments[2] {
			return 0, fmt.Errorf("clamp 的最小值不能大于最大值")
		}
		return math.Min(math.Max(arguments[0], arguments[1]), arguments[2]), nil
	case "rate":
		if len(arguments) != 1 {
			return 0, fmt.Errorf("函数 rate 需要 1 个参数")
		}
		if p.validateOnly {
			return 1, nil
		}
		index := int(arguments[0])
		if arguments[0] != float64(index) || index < 0 {
			return 0, fmt.Errorf("rate 的下标必须是非负整数")
		}
		return p.rateAt(index), nil
	default:
		return 0, fmt.Errorf("公式函数 %q 不受支持", name)
	}
}

func (p *rateExpression) rateAt(index int) float64 {
	if index < 0 || index >= len(p.rates) {
		return 0
	}
	return p.rates[index]
}

func (p *rateExpression) peek() rateToken {
	if p.index >= len(p.tokens) {
		return rateToken{kind: rateTokenEOF, pos: len(p.input)}
	}
	return p.tokens[p.index]
}

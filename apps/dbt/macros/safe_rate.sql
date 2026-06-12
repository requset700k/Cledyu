{% macro safe_rate(numerator, denominator, precision=4) %}
ROUND(SAFE_DIVIDE({{ numerator }}, {{ denominator }}), {{ precision }})
{% endmacro %}
